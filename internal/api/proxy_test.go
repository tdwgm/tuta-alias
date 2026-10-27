package api

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// resetProxy restores direct mode after a test: the proxy is package state,
// and a leftover one would reroute the other tests' httptest servers.
func resetProxy(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = SetProxy("") })
}

func TestParseProxy(t *testing.T) {
	valid := map[string]string{
		"":                                "",
		"   ":                             "",
		"socks5://127.0.0.1:1055":         "socks5://127.0.0.1:1055",
		" socks5://127.0.0.1:1055 ":       "socks5://127.0.0.1:1055",
		"SOCKS5://127.0.0.1:1055":         "socks5://127.0.0.1:1055",
		"socks5h://127.0.0.1:1055":        "socks5h://127.0.0.1:1055",
		"socks5://127.0.0.1:1055/":        "socks5://127.0.0.1:1055/",
		"socks5://user:pw@127.0.0.1:1055": "socks5://user:pw@127.0.0.1:1055",
		"socks5://[::1]:1055":             "socks5://[::1]:1055",
		"http://proxy.lan:3128":           "http://proxy.lan:3128",
		"https://proxy.lan:443":           "https://proxy.lan:443",
	}
	for in, want := range valid {
		u, err := ParseProxy(in)
		if err != nil {
			t.Errorf("ParseProxy(%q) error: %v", in, err)
			continue
		}
		got := ""
		if u != nil {
			got = u.String()
		}
		if got != want {
			t.Errorf("ParseProxy(%q) = %q, want %q", in, got, want)
		}
	}

	invalid := []string{
		"127.0.0.1:1055",              // no scheme
		"localhost:1055",              // parses as scheme "localhost"
		"socks4://127.0.0.1:1055",     // unsupported by net/http
		"ftp://127.0.0.1:21",          // unsupported
		"socks5://127.0.0.1",          // no port
		"socks5://:1055",              // no host
		"socks5://127.0.0.1:0",        // port out of range
		"socks5://127.0.0.1:65536",    // port out of range
		"socks5://127.0.0.1:1055/x",   // path
		"socks5://127.0.0.1:1055?x=1", // query
		"socks5://127.0.0.1:1055#x",   // fragment
		"socks5://%zz",                // not a URL
	}
	for _, in := range invalid {
		if _, err := ParseProxy(in); err == nil {
			t.Errorf("ParseProxy(%q) accepted, want error", in)
		} else if !strings.Contains(err.Error(), "TUTA_PROXY") {
			t.Errorf("ParseProxy(%q) error %q does not name TUTA_PROXY", in, err)
		}
	}
}

// A proxy password must never reach an error message.
func TestParseProxy_ErrorRedactsPassword(t *testing.T) {
	for _, in := range []string{
		"socks4://user:s3cretpw@127.0.0.1:1055",
		"socks5://user:s3cretpw@127.0.0.1",
		"socks5://user:s3cretpw@%zz:1055",
	} {
		_, err := ParseProxy(in)
		if err == nil {
			t.Fatalf("ParseProxy(%q) accepted", in)
		}
		if strings.Contains(err.Error(), "s3cretpw") {
			t.Errorf("error leaks the password: %v", err)
		}
	}
}

func TestNewClient_NoProxy_DefaultTransport(t *testing.T) {
	resetProxy(t)
	if err := SetProxy(""); err != nil {
		t.Fatal(err)
	}
	if rt := NewClient("https://example.invalid", "").HTTP.Transport; rt != nil {
		t.Errorf("Transport = %T, want nil (http.DefaultTransport, as before TUTA_PROXY)", rt)
	}
}

// With an HTTP proxy set, the request for an unresolvable host reaches the
// proxy (absolute-form URL) — proof that it never went direct.
func TestNewClient_SendsThroughHTTPProxy(t *testing.T) {
	resetProxy(t)
	var gotURL, gotToken string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL, gotToken = r.URL.String(), r.Header.Get("accessToken")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()
	if err := SetProxy(proxy.URL); err != nil {
		t.Fatal(err)
	}

	c := NewClient("http://tuta.invalid", "tok123")
	if _, err := c.ServiceRequest("POST", "sys", "SessionService", "", nil, ""); err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	if gotURL != "http://tuta.invalid/rest/sys/sessionservice" {
		t.Errorf("proxy saw URL %q", gotURL)
	}
	if gotToken != "tok123" {
		t.Errorf("proxy saw accessToken %q", gotToken)
	}
}

// fakeSOCKS5 is a minimal no-auth SOCKS5 server. It records the CONNECT
// target; with backend set it relays to it, otherwise it answers
// "general SOCKS server failure" (REP 1).
type fakeSOCKS5 struct {
	ln      net.Listener
	backend string
	mu      sync.Mutex
	targets []string
}

func newFakeSOCKS5(t *testing.T, backend string) *fakeSOCKS5 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSOCKS5{ln: ln, backend: backend}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *fakeSOCKS5) serve(c net.Conn) {
	defer c.Close()
	hdr := make([]byte, 2) // VER NMETHODS
	if _, err := io.ReadFull(c, hdr); err != nil {
		return
	}
	if _, err := io.ReadFull(c, make([]byte, hdr[1])); err != nil {
		return
	}
	c.Write([]byte{5, 0})  // no auth
	req := make([]byte, 4) // VER CMD RSV ATYP
	if _, err := io.ReadFull(c, req); err != nil {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		io.ReadFull(c, b)
		host = net.IP(b).String()
	case 3:
		n := make([]byte, 1)
		io.ReadFull(c, n)
		b := make([]byte, n[0])
		io.ReadFull(c, b)
		host = "name:" + string(b) // a hostname: the proxy resolves it
	case 4:
		b := make([]byte, 16)
		io.ReadFull(c, b)
		host = net.IP(b).String()
	}
	pb := make([]byte, 2)
	io.ReadFull(c, pb)
	s.mu.Lock()
	s.targets = append(s.targets, host+":"+strconv.Itoa(int(binary.BigEndian.Uint16(pb))))
	s.mu.Unlock()

	reply := []byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	if s.backend == "" {
		reply[1] = 1
		c.Write(reply)
		return
	}
	up, err := net.Dial("tcp", s.backend)
	if err != nil {
		reply[1] = 5
		c.Write(reply)
		return
	}
	defer up.Close()
	c.Write(reply)
	go io.Copy(up, c)
	io.Copy(c, up)
}

func (s *fakeSOCKS5) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

// socks5:// hands the hostname to the proxy (no local DNS lookup) and the
// response comes back through it.
func TestNewClient_SOCKS5_ProxyResolvesHostname(t *testing.T) {
	resetProxy(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	socks := newFakeSOCKS5(t, strings.TrimPrefix(backend.URL, "http://"))
	if err := SetProxy("socks5://" + socks.ln.Addr().String()); err != nil {
		t.Fatal(err)
	}

	c := NewClient("http://tuta.invalid:8080", "")
	if _, err := c.ServiceRequest("POST", "sys", "SessionService", "", nil, ""); err != nil {
		t.Fatalf("request through socks5: %v", err)
	}
	if got := socks.seen(); len(got) != 1 || got[0] != "name:tuta.invalid:8080" {
		t.Errorf("SOCKS CONNECT targets = %v, want [name:tuta.invalid:8080]", got)
	}
}

// A proxy that is not listening gives a readable error naming it — and not
// the request URL, whose ?_body= can carry the access token.
func TestNewClient_ProxyUnreachable_ReadableError(t *testing.T) {
	resetProxy(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if err := SetProxy("socks5://" + addr); err != nil {
		t.Fatal(err)
	}

	c := NewClient("https://tuta.invalid", "tok123")
	_, err = c.ServiceRequest("GET", "sys", "SaltService", "SaltData",
		map[string]any{"_format": 0, "mailAddress": "u@x.com"}, "SaltReturn")
	var pe *ProxyError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v (%T), want *ProxyError", err, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "TUTA_PROXY socks5://"+addr+" unreachable") {
		t.Errorf("message = %q", msg)
	}
	for _, leak := range []string{"_body", "tok123", "tuta.invalid"} {
		if strings.Contains(msg, leak) {
			t.Errorf("message leaks %q: %s", leak, msg)
		}
	}
}

// A SOCKS proxy that is up but cannot reach Tuta says so.
func TestNewClient_SOCKSConnectFails_ReadableError(t *testing.T) {
	resetProxy(t)
	socks := newFakeSOCKS5(t, "")
	proxy := "socks5://" + socks.ln.Addr().String()
	if err := SetProxy(proxy); err != nil {
		t.Fatal(err)
	}
	_, err := NewClient("https://tuta.invalid", "").ServiceRequest("POST", "sys", "SessionService", "", nil, "")
	if err == nil || !strings.Contains(err.Error(), "TUTA_PROXY "+proxy+" could not connect to tuta.invalid") {
		t.Errorf("err = %v", err)
	}
}

// Fail-closed: an invalid TUTA_PROXY fails every request and never dials
// the target directly. Clearing it restores direct mode.
func TestSetProxy_Invalid_FailsClosed(t *testing.T) {
	resetProxy(t)
	hits := 0
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer direct.Close()

	if err := SetProxy("socks4://127.0.0.1:1055"); err == nil {
		t.Fatal("SetProxy accepted socks4://")
	}
	_, err := NewClient(direct.URL, "").ServiceRequest("POST", "sys", "SessionService", "", nil, "")
	if err == nil || !strings.Contains(err.Error(), "invalid TUTA_PROXY") {
		t.Errorf("err = %v, want the invalid-TUTA_PROXY error", err)
	}
	if hits != 0 {
		t.Fatalf("direct server hit %d times with an invalid proxy", hits)
	}

	if err := SetProxy(""); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(direct.URL, "").ServiceRequest("POST", "sys", "SessionService", "", nil, ""); err != nil || hits != 1 {
		t.Errorf("after clearing: err=%v hits=%d, want direct request", err, hits)
	}
}

// FailClosed (unreadable config) blocks requests the same way.
func TestFailClosed_BlocksRequests(t *testing.T) {
	resetProxy(t)
	hits := 0
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer direct.Close()
	FailClosed(errors.New("cannot read TUTA_PROXY: permission denied"))
	if _, err := NewClient(direct.URL, "").ServiceRequest("POST", "sys", "SessionService", "", nil, ""); err == nil {
		t.Error("request succeeded while failed closed")
	}
	if hits != 0 {
		t.Errorf("direct server hit %d times", hits)
	}
}
