package api

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// proxyKey is the config key TUTA_PROXY comes from; named in every error.
const proxyKey = "TUTA_PROXY"

// proxySchemes are the proxy schemes net/http's Transport supports
// (Transport.Proxy, $GOROOT/src/net/http/transport.go). socks5 and socks5h
// behave the same there: the target hostname is sent to the proxy, which
// resolves it — no local DNS lookup for Tuta's hosts.
var proxySchemes = map[string]bool{"socks5": true, "socks5h": true, "http": true, "https": true}

var (
	proxyMu sync.RWMutex
	// proxyRT is the transport every new Client uses. nil = http.DefaultTransport,
	// i.e. no TUTA_PROXY (exactly the behaviour before the option existed).
	proxyRT http.RoundTripper
)

// ProxyError is a request failure caused by TUTA_PROXY itself (an invalid
// value, or a proxy that cannot be reached). It names the proxy, with any
// credentials redacted, instead of a raw dial error.
type ProxyError struct {
	Msg string
	Err error
}

func (e *ProxyError) Error() string {
	if e.Err == nil {
		return e.Msg
	}
	return e.Msg + " (" + e.Err.Error() + ")"
}

func (e *ProxyError) Unwrap() error { return e.Err }

// ParseProxy validates a TUTA_PROXY value: scheme://host:port with one of the
// schemes net/http supports, optional user:password@, nothing after the port.
// An empty value returns (nil, nil) = no proxy.
func ParseProxy(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		// url.Error repeats the raw value, which may hold a password.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, &ProxyError{Msg: fmt.Sprintf("invalid %s: not a URL like socks5://127.0.0.1:1055 (%v)", proxyKey, err)}
	}
	bad := func(reason string) (*url.URL, error) {
		return nil, &ProxyError{Msg: fmt.Sprintf("invalid %s %q: %s", proxyKey, u.Redacted(), reason)}
	}
	if !proxySchemes[u.Scheme] {
		return bad("want socks5://host:port, socks5h://, http:// or https://")
	}
	if u.Opaque != "" || u.Hostname() == "" {
		return bad("missing host")
	}
	if p, err := strconv.Atoi(u.Port()); err != nil || p < 1 || p > 65535 {
		return bad("missing or invalid port")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return bad("nothing may follow host:port")
	}
	return u, nil
}

// SetProxy makes every Client created afterwards send its requests through
// raw (a TUTA_PROXY value). "" = direct. An invalid value returns the error
// AND makes every later request fail with it: a proxy the user asked for is
// never silently skipped.
func SetProxy(raw string) error {
	u, err := ParseProxy(raw)
	switch {
	case err != nil:
		setTransport(failingTransport{err})
	case u == nil:
		setTransport(nil)
	default:
		t := &http.Transport{}
		if dt, ok := http.DefaultTransport.(*http.Transport); ok {
			t = dt.Clone()
		}
		t.Proxy = http.ProxyURL(u)
		setTransport(&proxyTransport{base: t, proxy: u.Redacted()})
	}
	return err
}

// FailClosed makes every request of Clients created afterwards fail with err.
// For when TUTA_PROXY cannot be determined (an unreadable config file).
func FailClosed(err error) { setTransport(failingTransport{err}) }

func setTransport(rt http.RoundTripper) {
	proxyMu.Lock()
	old := proxyRT
	proxyRT = rt
	proxyMu.Unlock()
	if pt, ok := old.(*proxyTransport); ok {
		pt.base.CloseIdleConnections()
	}
}

func currentTransport() http.RoundTripper {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	return proxyRT
}

// proxyTransport is one shared proxied pool (a Transport per Client would
// open a new proxy connection per TUI fetch / serve request) that turns
// proxy failures into a readable ProxyError.
type proxyTransport struct {
	base  *http.Transport
	proxy string // redacted proxy URL
}

func (p *proxyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := p.base.RoundTrip(r)
	if err == nil {
		return resp, nil
	}
	// net/http wraps a failed dial/TLS to the proxy as Op "proxyconnect";
	// a SOCKS proxy that is up but cannot reach the target as "socks connect".
	var oe *net.OpError
	if errors.As(err, &oe) {
		switch oe.Op {
		case "proxyconnect":
			return nil, &ProxyError{Msg: fmt.Sprintf("%s %s unreachable — is the proxy running?", proxyKey, p.proxy), Err: oe.Err}
		case "socks connect":
			return nil, &ProxyError{Msg: fmt.Sprintf("%s %s could not connect to %s", proxyKey, p.proxy, r.URL.Host), Err: oe.Err}
		}
	}
	return nil, err
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the pool.
func (p *proxyTransport) CloseIdleConnections() { p.base.CloseIdleConnections() }

// failingTransport refuses every request without dialing anything.
type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		r.Body.Close() // RoundTripper contract
	}
	return nil, f.err
}
