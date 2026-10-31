// Package outbound owns the process-wide appliance egress policy. Only a
// loopback proxy capability delivered by the supervisor's inherited FD can be
// installed; ambient proxy variables are never consulted.
package outbound

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
)

type Policy struct {
	proxy      *url.URL
	generation string
	disabled   bool
}

var active atomic.Pointer[Policy]

func NewPolicy(rawProxy, generation string) (*Policy, error) {
	if len(generation) != 64 {
		return nil, errors.New("appliance network generation is invalid")
	}
	for _, value := range generation {
		if (value < '0' || value > '9') && (value < 'a' || value > 'f') {
			return nil, errors.New("appliance network generation is invalid")
		}
	}
	parsed, err := url.Parse(rawProxy)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() != "127.0.0.1" ||
		parsed.Port() == "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, errors.New("appliance network proxy is invalid")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || parsed.Host != net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) {
		return nil, errors.New("appliance network proxy is invalid")
	}
	return &Policy{proxy: parsed, generation: generation}, nil
}

func (p *Policy) Proxy(request *http.Request) (*url.URL, error) {
	if p == nil || request == nil || request.URL == nil {
		return nil, errors.New("appliance outbound policy is unavailable")
	}
	if p.disabled || p.proxy == nil {
		return nil, errors.New("appliance outbound policy is unavailable")
	}
	host := request.URL.Hostname()
	if request.URL.Scheme != "https" || host == "" || strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil {
		return nil, errors.New("remote appliance requests require an HTTPS FQDN")
	}
	copy := *p.proxy
	return &copy, nil
}

func Install(rawProxy, generation string) error {
	policy, err := NewPolicy(rawProxy, generation)
	if err != nil {
		return err
	}
	return install(policy)
}

// InstallDisabled is used only by a descriptor-authenticated maintenance
// health process launched by a pre-network-capability supervisor. Every
// request through this global policy fails closed; local control traffic uses
// its own fixed no-proxy transport instead.
func InstallDisabled() error {
	return install(&Policy{disabled: true})
}

func install(policy *Policy) error {
	if policy == nil {
		return errors.New("appliance outbound policy is unavailable")
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return errors.New("default HTTP transport is unsupported")
	}
	configured := transport.Clone()
	configured.Proxy = policy.Proxy
	active.Store(policy)
	http.DefaultTransport = configured
	http.DefaultClient = &http.Client{Transport: configured}
	return nil
}

func Configured() bool {
	policy := active.Load()
	return policy != nil && policy.proxy != nil && !policy.disabled
}

// BridgeURL returns the canonical loopback proxy URL only when it came from a
// currently installed, generation-validated appliance policy. Callers that
// need to configure a non-Go client must not fall back to ambient proxy
// variables or a descriptor value copied outside this package.
func BridgeURL() (string, bool) {
	policy := active.Load()
	if policy == nil || policy.disabled || policy.proxy == nil {
		return "", false
	}

	raw := policy.proxy.String()
	validated, err := NewPolicy(raw, policy.generation)
	if err != nil || validated.proxy.String() != raw {
		return "", false
	}
	return raw, true
}

// ApplyToTransport permits a credential-bearing client to use the appliance
// bridge only after Install validated the inherited FD capability. Outside the
// appliance it keeps the caller's historical fail-closed no-proxy behavior.
func ApplyToTransport(transport *http.Transport) {
	if transport == nil {
		return
	}
	if policy := active.Load(); policy != nil {
		transport.Proxy = policy.Proxy
	} else {
		transport.Proxy = nil
	}
}
