package appliance

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

const (
	backendDialTimeout      = 3 * time.Second
	backendControlTimeout   = 10 * time.Second
	backendReadinessTimeout = 15 * time.Second
)

// newBackendDialContext is the only TCP dialer used for the appliance
// backend. WSL's 127.0.0.1 forwarding path is distinct from the listener
// address in newer releases, so the source and destination must be the same
// allowlisted loopback address. The returned function also validates the
// address supplied by net/http before it reaches the OS dialer.
func newBackendDialContext(host string, timeout time.Duration) (func(context.Context, string, string) (net.Conn, error), error) {
	if err := validateApplianceBackendHost(host); err != nil {
		return nil, err
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return nil, errInvalidApplianceBackendHost
	}
	if timeout <= 0 {
		timeout = backendDialTimeout
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		if network != "tcp" && network != "tcp4" {
			return nil, errors.New("appliance backend network is invalid")
		}
		addressHost, portText, err := net.SplitHostPort(address)
		if err != nil || addressHost != host {
			return nil, errors.New("appliance backend address is invalid")
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("appliance backend port is invalid")
		}
		dialer := net.Dialer{
			Timeout:   timeout,
			KeepAlive: -1,
			LocalAddr: &net.TCPAddr{IP: append(net.IP(nil), ip...)},
		}
		return dialer.DialContext(ctx, "tcp4", net.JoinHostPort(host, strconv.Itoa(port)))
	}, nil
}

func dialBackendTCP(ctx context.Context, host string, port int) (net.Conn, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("appliance backend port is invalid")
	}
	dial, err := newBackendDialContext(host, backendDialTimeout)
	if err != nil {
		return nil, err
	}
	return dial(ctx, "tcp4", net.JoinHostPort(host, strconv.Itoa(port)))
}

func newBackendHTTPClient(host string) (*http.Client, error) {
	dial, err := newBackendDialContext(host, backendDialTimeout)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            dial,
			DisableKeepAlives:      true,
			ForceAttemptHTTP2:      false,
			ResponseHeaderTimeout:  backendControlTimeout,
			TLSHandshakeTimeout:    backendDialTimeout,
			ExpectContinueTimeout:  time.Second,
			MaxResponseHeaderBytes: 64 * 1024,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func backendURL(host string, port int, endpoint string) (string, error) {
	if err := validateApplianceBackendHost(host); err != nil {
		return "", err
	}
	if port < 1 || port > 65535 {
		return "", errors.New("appliance backend port is invalid")
	}
	if endpoint == "" || endpoint[0] != '/' {
		return "", errors.New("appliance backend endpoint is invalid")
	}
	return fmt.Sprintf("http://%s%s", net.JoinHostPort(host, strconv.Itoa(port)), endpoint), nil
}
