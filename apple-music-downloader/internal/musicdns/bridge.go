// Package musicdns provides a private IPv4 lookup for Music session verification.
// It is not a general DNS forwarder and never carries Apple credentials.
package musicdns

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	Host          = "api.music.apple.com"
	FWConfig      = "opt/amdl/music-dns-port"
	guestGateway  = "10.0.2.2"
	lookupPath    = "/v1/music-storefront-ipv4"
	lookupTimeout = 2 * time.Second
	maxIPs        = 8
	maxResponse   = 512
)

var errUnavailable = errors.New("music DNS bridge unavailable")

type systemLookup func(context.Context, string, string) ([]netip.Addr, error)
type Lookup func(context.Context) ([]netip.Addr, error)

// Listen binds only to Windows loopback, reached through the QEMU guest gateway.
// The per-boot capability is independent of Apple credentials.
func Listen(capability string) (int, io.Closer, error) {
	if len(capability) != 64 {
		return 0, nil, errUnavailable
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return 0, nil, err
	}
	server := &http.Server{
		Handler:           handler(capability, (&net.Resolver{}).LookupNetIP),
		ReadHeaderTimeout: time.Second, ReadTimeout: time.Second,
		WriteTimeout: lookupTimeout + time.Second, MaxHeaderBytes: 4096,
	}
	server.SetKeepAlivesEnabled(false)
	go func() { _ = server.Serve(listener) }()
	// Close the listener even if the controller stops before Serve starts.
	return listener.Addr().(*net.TCPAddr).Port, bridgeCloser{server, listener}, nil
}

type bridgeCloser struct {
	server   *http.Server
	listener net.Listener
}

func (c bridgeCloser) Close() error {
	_ = c.server.Close()
	_ = c.listener.Close()
	return nil
}

func handler(capability string, resolve systemLookup) http.Handler {
	busy := make(chan struct{}, 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.RequestURI != lookupPath || r.ContentLength != 0 ||
			subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+capability)) != 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		select {
		case busy <- struct{}{}:
			defer func() { <-busy }()
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
		defer cancel()
		ips, err := resolve(ctx, "ip4", Host)
		ips = usableIPs(ips)
		if err != nil || len(ips) == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ips)
	})
}

// GuestLookup returns nil for old or isolated launches without the bridge.
func GuestLookup(capability string) Lookup {
	raw, err := os.ReadFile("/sys/firmware/qemu_fw_cfg/by_name/" + FWConfig + "/raw")
	if err != nil || len(raw) > 6 || len(capability) != 64 {
		return nil
	}
	port, err := strconv.Atoi(strings.TrimSuffix(string(raw), "\x00"))
	if err != nil || port < 1 || port > 65535 {
		return nil
	}
	return remoteLookup(net.JoinHostPort(guestGateway, strconv.Itoa(port)), capability)
}

func remoteLookup(address, capability string) Lookup {
	client := &http.Client{
		Transport:     &http.Transport{DisableKeepAlives: true},
		Timeout:       lookupTimeout + 250*time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return func(ctx context.Context) ([]netip.Addr, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+lookupPath, nil)
		if err != nil {
			return nil, errUnavailable
		}
		req.Header.Set("Authorization", "Bearer "+capability)
		resp, err := client.Do(req)
		if err != nil {
			return nil, errUnavailable
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, errUnavailable
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
		var ips []netip.Addr
		if err != nil || len(body) > maxResponse || json.Unmarshal(body, &ips) != nil || len(ips) > maxIPs {
			return nil, errUnavailable
		}
		if ips = usableIPs(ips); len(ips) == 0 {
			return nil, errUnavailable
		}
		return ips, nil
	}
}

func usableIPs(input []netip.Addr) []netip.Addr {
	var out []netip.Addr
	for _, ip := range input {
		ip = ip.Unmap()
		if !ip.Is4() || !ip.IsGlobalUnicast() {
			continue
		}
		found := false
		for _, old := range out {
			found = found || old == ip
		}
		if !found {
			out = append(out, ip)
			if len(out) == maxIPs {
				break
			}
		}
	}
	return out
}
