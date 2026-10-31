//go:build windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	egressConnectStdioAction = "egress-connect-stdio"

	maxEgressFrameBytes       = 4 * 1024
	maxEgressProxyHeaderBytes = 16 * 1024
	egressHandshakeTimeout    = 20 * time.Second
	egressIdleTimeout         = 2 * time.Minute

	egressPathDirect    = "direct"
	egressPathHTTPProxy = "http_proxy"

	egressErrorInvalidArguments    = "invalid_arguments"
	egressErrorInvalidRequest      = "invalid_request"
	egressErrorProxyResolution     = "proxy_resolution_failed"
	egressErrorTargetResolution    = "target_resolution_failed"
	egressErrorConnect             = "connect_failed"
	egressErrorProxyAuthentication = "proxy_authentication_required"
	egressErrorStream              = "stream_failed"

	egressConnectTokenInvalidArguments = "AMDL_EGRESS_INVALID_ARGUMENTS"
	egressConnectTokenInvalidRequest   = "AMDL_EGRESS_INVALID_REQUEST"
	egressConnectTokenProxy            = "AMDL_EGRESS_PROXY_FAILED"
	egressConnectTokenResolve          = "AMDL_EGRESS_RESOLVE_FAILED"
	egressConnectTokenConnect          = "AMDL_EGRESS_CONNECT_FAILED"
	egressConnectTokenProxyAuth        = "AMDL_EGRESS_PROXY_AUTH_REQUIRED"
	egressConnectTokenStream           = "AMDL_EGRESS_STREAM_FAILED"

	egressConnectExitSuccess          = 0
	egressConnectExitInvalidArguments = 40
	egressConnectExitInvalidRequest   = 41
	egressConnectExitProxy            = 42
	egressConnectExitResolve          = 43
	egressConnectExitConnect          = 44
	egressConnectExitProxyAuth        = 45
	egressConnectExitStream           = 46

	winHTTPAccessTypeNoProxy    = 1
	winHTTPAccessTypeNamedProxy = 3

	winHTTPAutoProxyAutoDetect   = 0x00000001
	winHTTPAutoProxyConfigURL    = 0x00000002
	winHTTPAutoDetectTypeDHCP    = 0x00000001
	winHTTPAutoDetectTypeDNSA    = 0x00000002
	winHTTPOpenAccessTypeNoProxy = 1
	winHTTPTimeoutMilliseconds   = 10_000
)

var (
	winHTTPDLL                             = windows.NewLazySystemDLL("winhttp.dll")
	procWinHTTPOpen                        = winHTTPDLL.NewProc("WinHttpOpen")
	procWinHTTPCloseHandle                 = winHTTPDLL.NewProc("WinHttpCloseHandle")
	procWinHTTPSetTimeouts                 = winHTTPDLL.NewProc("WinHttpSetTimeouts")
	procWinHTTPGetIEProxyConfigCurrentUser = winHTTPDLL.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	procWinHTTPGetProxyForURL              = winHTTPDLL.NewProc("WinHttpGetProxyForUrl")
	procGlobalFree                         = kernel32.NewProc("GlobalFree")
)

type egressConnectRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
}

type egressConnectResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	OK            bool   `json:"ok"`
	Path          string `json:"path,omitempty"`
	Error         string `json:"error,omitempty"`
}

type egressConnectOutcome struct {
	Token string
	Code  int
}

type egressProxyDecision struct {
	Path         string
	ProxyAddress string
}

type egressConnectDependencies struct {
	resolveProxy func(context.Context, string) (egressProxyDecision, error)
	resolveHost  func(context.Context, string) ([]netip.Addr, error)
	dial         func(context.Context, string, string) (net.Conn, error)
}

type windowsProxyConfig struct {
	AutoDetect    bool
	AutoConfigURL string
	Proxy         string
	ProxyBypass   string
}

type windowsProxyInfo struct {
	AccessType uint32
	Proxy      string
}

type windowsProxyLookup func(string, windowsProxyConfig) (windowsProxyInfo, error)

type winHTTPCurrentUserProxyConfig struct {
	AutoDetect    int32
	AutoConfigURL *uint16
	Proxy         *uint16
	ProxyBypass   *uint16
}

type winHTTPAutoProxyOptions struct {
	Flags                 uint32
	AutoDetectFlags       uint32
	AutoConfigURL         *uint16
	Reserved              unsafe.Pointer
	ReservedFlags         uint32
	AutoLogonIfChallenged int32
}

type winHTTPProxyInfo struct {
	AccessType uint32
	Proxy      *uint16
	Bypass     *uint16
}

var errEgressProxyAuthentication = errors.New("proxy authentication is unsupported")

func defaultEgressConnectDependencies() egressConnectDependencies {
	dialer := &net.Dialer{Timeout: egressHandshakeTimeout, KeepAlive: 30 * time.Second}
	return egressConnectDependencies{
		resolveProxy: func(ctx context.Context, host string) (egressProxyDecision, error) {
			if err := ctx.Err(); err != nil {
				return egressProxyDecision{}, err
			}
			config, err := currentWindowsProxyConfig()
			if err != nil {
				return egressProxyDecision{}, err
			}
			decision, err := resolveEffectiveWindowsProxy(host, config, lookupWinHTTPAutoProxy)
			if err != nil {
				return egressProxyDecision{}, err
			}
			if err := ctx.Err(); err != nil {
				return egressProxyDecision{}, err
			}
			return decision, nil
		},
		resolveHost: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		dial: dialer.DialContext,
	}
}

func runEgressConnectStdio(
	extraArgs []string,
	dependencies egressConnectDependencies,
	stdin io.Reader,
	stdout io.Writer,
) egressConnectOutcome {
	if len(extraArgs) != 0 {
		return writeEgressFailure(stdout, egressErrorInvalidArguments, egressConnectTokenInvalidArguments, egressConnectExitInvalidArguments)
	}
	request, err := readEgressConnectRequest(stdin)
	if err != nil {
		return writeEgressFailure(stdout, egressErrorInvalidRequest, egressConnectTokenInvalidRequest, egressConnectExitInvalidRequest)
	}
	if dependencies.resolveHost == nil || dependencies.resolveProxy == nil || dependencies.dial == nil {
		return writeEgressFailure(stdout, egressErrorConnect, egressConnectTokenConnect, egressConnectExitConnect)
	}

	ctx, cancel := context.WithTimeout(context.Background(), egressHandshakeTimeout)
	defer cancel()
	proxy, err := dependencies.resolveProxy(ctx, request.Host)
	if err != nil || !validEgressProxyDecision(proxy) {
		return writeEgressFailure(stdout, egressErrorProxyResolution, egressConnectTokenProxy, egressConnectExitProxy)
	}
	resolved, err := dependencies.resolveHost(ctx, request.Host)
	if err != nil {
		return writeEgressFailure(stdout, egressErrorTargetResolution, egressConnectTokenResolve, egressConnectExitResolve)
	}
	var addresses []netip.Addr
	if proxy.Path == egressPathDirect {
		addresses, err = validateEgressTargetAddresses(resolved)
	} else {
		addresses, err = validateEgressProxyTargetObservation(resolved)
	}
	if err != nil {
		return writeEgressFailure(stdout, egressErrorTargetResolution, egressConnectTokenResolve, egressConnectExitResolve)
	}

	connection, err := dialEgressTarget(ctx, dependencies.dial, addresses, proxy)
	if errors.Is(err, errEgressProxyAuthentication) {
		return writeEgressFailure(stdout, egressErrorProxyAuthentication, egressConnectTokenProxyAuth, egressConnectExitProxyAuth)
	}
	if err != nil {
		return writeEgressFailure(stdout, egressErrorConnect, egressConnectTokenConnect, egressConnectExitConnect)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return writeEgressFailure(stdout, egressErrorConnect, egressConnectTokenConnect, egressConnectExitConnect)
	}
	connection = &egressIdleConn{Conn: connection, timeout: egressIdleTimeout}
	if err := writeEgressResponse(stdout, egressConnectResponse{SchemaVersion: 1, OK: true, Path: proxy.Path}); err != nil {
		return egressConnectOutcome{Token: egressConnectTokenStream, Code: egressConnectExitStream}
	}
	if err := copyEgressDuplex(stdin, stdout, connection); err != nil {
		return egressConnectOutcome{Token: egressConnectTokenStream, Code: egressConnectExitStream}
	}
	return egressConnectOutcome{Code: egressConnectExitSuccess}
}

func writeEgressFailure(output io.Writer, reason, token string, code int) egressConnectOutcome {
	if err := writeEgressResponse(output, egressConnectResponse{SchemaVersion: 1, OK: false, Error: reason}); err != nil {
		return egressConnectOutcome{Token: egressConnectTokenStream, Code: egressConnectExitStream}
	}
	return egressConnectOutcome{Token: token, Code: code}
}

func readEgressConnectRequest(input io.Reader) (egressConnectRequest, error) {
	var request egressConnectRequest
	var header [4]byte
	if _, err := io.ReadFull(input, header[:]); err != nil {
		return request, errors.New("invalid request frame")
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maxEgressFrameBytes {
		return request, errors.New("invalid request frame")
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(input, payload); err != nil {
		return request, errors.New("invalid request frame")
	}
	if err := requireUniqueEgressJSONFields(payload); err != nil {
		return egressConnectRequest{}, errors.New("invalid request")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return egressConnectRequest{}, errors.New("invalid request")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) || request.SchemaVersion != 1 || request.Port != 443 {
		return egressConnectRequest{}, errors.New("invalid request")
	}
	host, ok := canonicalEgressFQDN(request.Host)
	if !ok {
		return egressConnectRequest{}, errors.New("invalid request")
	}
	request.Host = host
	return request, nil
}

func requireUniqueEgressJSONFields(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("request must be an object")
	}
	seen := make(map[string]struct{}, 3)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return errors.New("request key is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("request key is duplicated")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errors.New("request value is invalid")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("request object is invalid")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return errors.New("request contains trailing JSON")
	}
	return nil
}

func writeEgressResponse(output io.Writer, response egressConnectResponse) error {
	payload, err := json.Marshal(response)
	if err != nil || len(payload) == 0 || len(payload) > maxEgressFrameBytes {
		return errors.New("encode response")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeEgressBytes(output, header[:]); err != nil {
		return errors.New("write response")
	}
	if err := writeEgressBytes(output, payload); err != nil {
		return errors.New("write response")
	}
	return nil
}

func writeEgressBytes(output io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := output.Write(payload)
		if written < 0 || written > len(payload) {
			return errors.New("invalid write result")
		}
		payload = payload[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func canonicalEgressFQDN(value string) (string, bool) {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 253 || strings.HasSuffix(value, ".") || net.ParseIP(value) != nil {
		return "", false
	}
	host := strings.ToLower(value)
	if !strings.Contains(host, ".") {
		return "", false
	}
	for _, suffix := range []string{".local", ".localhost", ".localdomain", ".lan", ".home", ".internal", ".invalid", ".test", ".example"} {
		if strings.HasSuffix(host, suffix) {
			return "", false
		}
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return "", false
			}
		}
	}
	topLevelHasLetter := false
	for _, character := range labels[len(labels)-1] {
		if character >= 'a' && character <= 'z' {
			topLevelHasLetter = true
			break
		}
	}
	if !topLevelHasLetter {
		return "", false
	}
	return host, true
}

var syntheticProxyEgressPrefix = netip.MustParsePrefix("198.18.0.0/15")

var nonPublicEgressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	syntheticProxyEgressPrefix,
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func validateEgressTargetAddresses(addresses []netip.Addr) ([]netip.Addr, error) {
	if len(addresses) == 0 {
		return nil, errors.New("target did not resolve")
	}
	unique := make(map[netip.Addr]struct{}, len(addresses))
	for _, original := range addresses {
		address := original.Unmap()
		if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() ||
			address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
			return nil, errors.New("target resolved to a non-public address")
		}
		for _, prefix := range nonPublicEgressPrefixes {
			if prefix.Contains(address) {
				return nil, errors.New("target resolved to a non-public address")
			}
		}
		unique[address] = struct{}{}
	}
	result := make([]netip.Addr, 0, len(unique))
	for address := range unique {
		result = append(result, address)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Less(result[right]) })
	return result, nil
}

// A named Windows HTTP proxy receives an address from this validated set, not an
// FQDN that it could resolve a second time. This pins the CONNECT destination and
// rejects loopback/private rebinding. Mihomo fake-IP mode deliberately returns
// 198.18.0.0/15 for public names; that synthetic range is acceptable only on the
// named-proxy path and is never dialed directly by the helper.
func validateEgressProxyTargetObservation(addresses []netip.Addr) ([]netip.Addr, error) {
	if len(addresses) == 0 {
		return nil, errors.New("target did not resolve")
	}
	unique := make(map[netip.Addr]struct{}, len(addresses))
	for _, original := range addresses {
		address := original.Unmap()
		if address.IsValid() && syntheticProxyEgressPrefix.Contains(address) {
			unique[address] = struct{}{}
			continue
		}
		if _, err := validateEgressTargetAddresses([]netip.Addr{address}); err != nil {
			return nil, err
		}
		unique[address] = struct{}{}
	}
	result := make([]netip.Addr, 0, len(unique))
	for address := range unique {
		result = append(result, address)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Less(result[right]) })
	return result, nil
}

func validEgressProxyDecision(decision egressProxyDecision) bool {
	if decision.Path == egressPathDirect {
		return decision.ProxyAddress == ""
	}
	if decision.Path != egressPathHTTPProxy || decision.ProxyAddress == "" {
		return false
	}
	normalized, err := normalizeHTTPProxyEndpoint(decision.ProxyAddress)
	return err == nil && normalized == decision.ProxyAddress
}

func dialEgressTarget(
	ctx context.Context,
	dial func(context.Context, string, string) (net.Conn, error),
	addresses []netip.Addr,
	proxy egressProxyDecision,
) (net.Conn, error) {
	for _, address := range addresses {
		var connection net.Conn
		var err error
		if proxy.Path == egressPathDirect {
			connection, err = dial(ctx, "tcp", net.JoinHostPort(address.String(), "443"))
		} else {
			connection, err = openHTTPProxyTunnel(ctx, dial, proxy.ProxyAddress, address)
		}
		if err == nil {
			return connection, nil
		}
		if errors.Is(err, errEgressProxyAuthentication) {
			return nil, errEgressProxyAuthentication
		}
	}
	return nil, errors.New("all egress connection attempts failed")
}

func openHTTPProxyTunnel(
	ctx context.Context,
	dial func(context.Context, string, string) (net.Conn, error),
	proxyAddress string,
	target netip.Addr,
) (net.Conn, error) {
	if !target.IsValid() {
		return nil, errors.New("proxy target is invalid")
	}
	connection, err := dial(ctx, "tcp", proxyAddress)
	if err != nil {
		return nil, errors.New("proxy connection failed")
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = connection.Close()
		}
	}()
	if err := connection.SetDeadline(time.Now().Add(egressHandshakeTimeout)); err != nil {
		return nil, errors.New("proxy deadline failed")
	}
	authority := net.JoinHostPort(target.String(), "443")
	request := "CONNECT " + authority + " HTTP/1.1\r\n" +
		"Host: " + authority + "\r\n" +
		"Proxy-Connection: Keep-Alive\r\n\r\n"
	if err := writeEgressBytes(connection, []byte(request)); err != nil {
		return nil, errors.New("proxy request failed")
	}
	status, reader, err := readHTTPProxyResponse(connection)
	if err != nil {
		return nil, errors.New("proxy response failed")
	}
	if status == 407 {
		return nil, errEgressProxyAuthentication
	}
	if status != 200 {
		return nil, errors.New("proxy refused tunnel")
	}
	closeOnError = false
	return &egressBufferedConn{Conn: connection, reader: reader}, nil
}

func readHTTPProxyResponse(connection net.Conn) (int, *bufio.Reader, error) {
	reader := bufio.NewReaderSize(connection, 4096)
	header := make([]byte, 0, 512)
	for len(header) < maxEgressProxyHeaderBytes {
		value, err := reader.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		header = append(header, value)
		if bytes.HasSuffix(header, []byte("\r\n\r\n")) {
			break
		}
	}
	if !bytes.HasSuffix(header, []byte("\r\n\r\n")) {
		return 0, nil, errors.New("proxy response header exceeded limit")
	}
	lines := strings.Split(string(header[:len(header)-4]), "\r\n")
	if len(lines) < 1 {
		return 0, nil, errors.New("proxy response is empty")
	}
	parts := strings.SplitN(lines[0], " ", 3)
	if len(parts) < 2 || (parts[0] != "HTTP/1.1" && parts[0] != "HTTP/1.0") || len(parts[1]) != 3 {
		return 0, nil, errors.New("proxy status line is invalid")
	}
	status, err := strconv.Atoi(parts[1])
	if err != nil || status < 100 || status > 999 {
		return 0, nil, errors.New("proxy status is invalid")
	}
	for _, line := range lines[1:] {
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || !strings.Contains(line, ":") {
			return 0, nil, errors.New("proxy response header is invalid")
		}
		for _, character := range line {
			if character < 0x20 && character != '\t' || character == 0x7f {
				return 0, nil, errors.New("proxy response header is invalid")
			}
		}
	}
	return status, reader, nil
}

type egressBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (connection *egressBufferedConn) Read(payload []byte) (int, error) {
	return connection.reader.Read(payload)
}

func (connection *egressBufferedConn) CloseWrite() error {
	if closeWriter, ok := connection.Conn.(interface{ CloseWrite() error }); ok {
		return closeWriter.CloseWrite()
	}
	return nil
}

type egressIdleConn struct {
	net.Conn
	timeout time.Duration
}

func (connection *egressIdleConn) Read(payload []byte) (int, error) {
	if err := connection.Conn.SetReadDeadline(time.Now().Add(connection.timeout)); err != nil {
		return 0, err
	}
	return connection.Conn.Read(payload)
}

func (connection *egressIdleConn) Write(payload []byte) (int, error) {
	if err := connection.Conn.SetWriteDeadline(time.Now().Add(connection.timeout)); err != nil {
		return 0, err
	}
	return connection.Conn.Write(payload)
}

func (connection *egressIdleConn) CloseWrite() error {
	if closeWriter, ok := connection.Conn.(interface{ CloseWrite() error }); ok {
		return closeWriter.CloseWrite()
	}
	return nil
}

func copyEgressDuplex(input io.Reader, output io.Writer, connection net.Conn) error {
	uploadDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(connection, input)
		if closeWriter, ok := connection.(interface{ CloseWrite() error }); ok {
			if closeErr := closeWriter.CloseWrite(); err == nil {
				err = closeErr
			}
		}
		if err != nil {
			_ = connection.Close()
		}
		uploadDone <- err
	}()
	_, downloadErr := io.Copy(output, connection)
	_ = connection.Close()
	var (
		uploadErr       error
		uploadCompleted bool
	)
	select {
	case uploadErr = <-uploadDone:
		uploadCompleted = true
	default:
	}
	if !uploadCompleted {
		closer, canClose := input.(io.Closer)
		if !canClose {
			// The executable supplies os.Stdin, which is closable. Avoid a
			// permanent wait if an alternate embedder supplies a blocking reader
			// without cancellation support.
			if downloadErr != nil {
				return errors.New("egress stream failed")
			}
			return nil
		}
		_ = closer.Close()
		uploadErr = <-uploadDone
		// Closing the owned input is the normal cancellation path after the
		// remote end has already completed. Its resulting read error is not a
		// stream failure.
		uploadErr = nil
	}
	if downloadErr == nil && (errors.Is(uploadErr, io.ErrClosedPipe) || errors.Is(uploadErr, net.ErrClosed)) {
		// A proxy may close the full-duplex stream immediately after its reply.
		// If the download direction reached a clean EOF, the resulting local
		// half-close race is teardown, not loss of an accepted upload.
		uploadErr = nil
	}
	if downloadErr != nil || uploadErr != nil {
		return errors.New("egress stream failed")
	}
	return nil
}

func resolveEffectiveWindowsProxy(host string, config windowsProxyConfig, lookup windowsProxyLookup) (egressProxyDecision, error) {
	if _, ok := canonicalEgressFQDN(host); !ok {
		return egressProxyDecision{}, errors.New("proxy target is invalid")
	}
	if config.AutoConfigURL != "" || config.AutoDetect {
		if lookup == nil {
			return egressProxyDecision{}, errors.New("automatic proxy lookup is unavailable")
		}
		info, err := lookup("https://"+host+"/", config)
		if err != nil {
			return egressProxyDecision{}, errors.New("automatic proxy lookup failed")
		}
		return proxyDecisionFromWinHTTPInfo(info)
	}
	if windowsProxyBypassMatches(host, config.ProxyBypass) {
		return egressProxyDecision{Path: egressPathDirect}, nil
	}
	if strings.TrimSpace(config.Proxy) == "" {
		return egressProxyDecision{Path: egressPathDirect}, nil
	}
	return parseConfiguredWindowsProxy(config.Proxy)
}

func proxyDecisionFromWinHTTPInfo(info windowsProxyInfo) (egressProxyDecision, error) {
	switch info.AccessType {
	case winHTTPAccessTypeNoProxy:
		if strings.TrimSpace(info.Proxy) != "" {
			return egressProxyDecision{}, errors.New("DIRECT proxy result included an endpoint")
		}
		return egressProxyDecision{Path: egressPathDirect}, nil
	case winHTTPAccessTypeNamedProxy:
		return parseConfiguredWindowsProxy(info.Proxy)
	default:
		return egressProxyDecision{}, errors.New("unsupported WinHTTP proxy mode")
	}
}

func parseConfiguredWindowsProxy(value string) (egressProxyDecision, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return egressProxyDecision{}, errors.New("proxy endpoint is empty")
	}
	var generic, httpsEndpoint string
	for _, raw := range strings.Split(value, ";") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if strings.EqualFold(entry, "DIRECT") {
			if len(strings.Split(value, ";")) != 1 {
				return egressProxyDecision{}, errors.New("mixed proxy fallback is unsupported")
			}
			return egressProxyDecision{Path: egressPathDirect}, nil
		}
		if strings.Contains(entry, "=") {
			parts := strings.SplitN(entry, "=", 2)
			scheme := strings.ToLower(strings.TrimSpace(parts[0]))
			endpoint := strings.TrimSpace(parts[1])
			switch scheme {
			case "https":
				if httpsEndpoint != "" {
					return egressProxyDecision{}, errors.New("ambiguous HTTPS proxy")
				}
				httpsEndpoint = endpoint
			case "http":
				// HTTP-only entries do not apply to an HTTPS target.
			case "socks", "socks4", "socks5":
				return egressProxyDecision{}, errors.New("SOCKS proxy is unsupported")
			default:
				return egressProxyDecision{}, errors.New("unknown proxy scheme")
			}
			continue
		}
		if generic != "" {
			return egressProxyDecision{}, errors.New("proxy fallback lists are unsupported")
		}
		generic = entry
	}
	endpoint := httpsEndpoint
	if endpoint == "" {
		endpoint = generic
	}
	if endpoint == "" {
		return egressProxyDecision{}, errors.New("no HTTPS proxy endpoint")
	}
	normalized, err := normalizeHTTPProxyEndpoint(endpoint)
	if err != nil {
		return egressProxyDecision{}, err
	}
	return egressProxyDecision{Path: egressPathHTTPProxy, ProxyAddress: normalized}, nil
}

func normalizeHTTPProxyEndpoint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n\t ,") || strings.Contains(value, "@") {
		return "", errors.New("proxy endpoint is invalid")
	}
	parseValue := value
	if !strings.Contains(parseValue, "://") {
		parseValue = "http://" + parseValue
	}
	parsed, err := url.Parse(parseValue)
	if err != nil || !strings.EqualFold(parsed.Scheme, "http") || parsed.User != nil || parsed.Host == "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("proxy transport is unsupported")
	}
	host := parsed.Hostname()
	if host == "" || strings.ContainsRune(host, 0) {
		return "", errors.New("proxy host is invalid")
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", errors.New("proxy port is invalid")
	}
	return net.JoinHostPort(host, port), nil
}

func windowsProxyBypassMatches(host, bypass string) bool {
	for _, raw := range strings.Split(bypass, ";") {
		entry := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case entry == "" || entry == "<local>" || entry == "<-loopback>":
			continue
		case strings.HasPrefix(entry, "*.") && strings.HasSuffix(host, entry[1:]):
			return true
		case strings.HasPrefix(entry, ".") && strings.HasSuffix(host, entry):
			return true
		case host == entry:
			return true
		}
	}
	return false
}

func currentWindowsProxyConfig() (windowsProxyConfig, error) {
	var native winHTTPCurrentUserProxyConfig
	result, _, _ := procWinHTTPGetIEProxyConfigCurrentUser.Call(uintptr(unsafe.Pointer(&native)))
	if result == 0 {
		return windowsProxyConfig{}, errors.New("read current-user proxy configuration")
	}
	defer freeWinHTTPString(native.AutoConfigURL)
	defer freeWinHTTPString(native.Proxy)
	defer freeWinHTTPString(native.ProxyBypass)
	return windowsProxyConfig{
		AutoDetect:    native.AutoDetect != 0,
		AutoConfigURL: utf16String(native.AutoConfigURL),
		Proxy:         utf16String(native.Proxy),
		ProxyBypass:   utf16String(native.ProxyBypass),
	}, nil
}

func lookupWinHTTPAutoProxy(targetURL string, config windowsProxyConfig) (windowsProxyInfo, error) {
	agent, _ := windows.UTF16PtrFromString("AppleMusicDownloader-Egress/1")
	session, _, _ := procWinHTTPOpen.Call(
		uintptr(unsafe.Pointer(agent)),
		winHTTPOpenAccessTypeNoProxy,
		0,
		0,
		0,
	)
	if session == 0 {
		return windowsProxyInfo{}, errors.New("open WinHTTP session")
	}
	defer procWinHTTPCloseHandle.Call(session)
	result, _, _ := procWinHTTPSetTimeouts.Call(
		session,
		winHTTPTimeoutMilliseconds,
		winHTTPTimeoutMilliseconds,
		winHTTPTimeoutMilliseconds,
		winHTTPTimeoutMilliseconds,
	)
	if result == 0 {
		return windowsProxyInfo{}, errors.New("configure WinHTTP timeouts")
	}
	options := winHTTPAutoProxyOptions{AutoLogonIfChallenged: 0}
	var configURL *uint16
	if config.AutoConfigURL != "" {
		var err error
		configURL, err = windows.UTF16PtrFromString(config.AutoConfigURL)
		if err != nil {
			return windowsProxyInfo{}, errors.New("encode automatic proxy URL")
		}
		options.Flags = winHTTPAutoProxyConfigURL
		options.AutoConfigURL = configURL
	} else if config.AutoDetect {
		options.Flags = winHTTPAutoProxyAutoDetect
		options.AutoDetectFlags = winHTTPAutoDetectTypeDHCP | winHTTPAutoDetectTypeDNSA
	} else {
		return windowsProxyInfo{}, errors.New("automatic proxy is not configured")
	}
	urlPointer, err := windows.UTF16PtrFromString(targetURL)
	if err != nil {
		return windowsProxyInfo{}, errors.New("encode proxy target")
	}
	var native winHTTPProxyInfo
	result, _, _ = procWinHTTPGetProxyForURL.Call(
		session,
		uintptr(unsafe.Pointer(urlPointer)),
		uintptr(unsafe.Pointer(&options)),
		uintptr(unsafe.Pointer(&native)),
	)
	if result == 0 {
		return windowsProxyInfo{}, errors.New("resolve automatic proxy")
	}
	defer freeWinHTTPString(native.Proxy)
	defer freeWinHTTPString(native.Bypass)
	return windowsProxyInfo{AccessType: native.AccessType, Proxy: utf16String(native.Proxy)}, nil
}

func utf16String(value *uint16) string {
	if value == nil {
		return ""
	}
	return windows.UTF16PtrToString(value)
}

func freeWinHTTPString(value *uint16) {
	if value != nil {
		_, _, _ = procGlobalFree.Call(uintptr(unsafe.Pointer(value)))
	}
}
