package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tdwgm/tuta-alias/internal/consts"
)

// resetClientVersion isolates the package-level cv state and its cache file.
func resetClientVersion(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	cvMu.Lock()
	cvLoaded, cvValue = false, ""
	cvMu.Unlock()
	t.Cleanup(func() {
		cvMu.Lock()
		cvLoaded, cvValue = false, ""
		cvMu.Unlock()
	})
	return filepath.Join(dir, "tuta-alias", "client-version")
}

// tutaStub serves index.js with liveCV and rejects any other cv with 474.
func tutaStub(t *testing.T, liveCV string, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.js" {
			w.Write([]byte(`window.whitelabelCustomizations=null;env={"versionNumber": "` + liveCV + `","mode":"Browser"}`))
			return
		}
		*seen = append(*seen, r.Header.Get("cv"))
		if r.Header.Get("cv") != liveCV {
			w.Header().Set("error-id", "abc")
			w.WriteHeader(StatusInvalidSoftwareVersion)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"421": "0", "422": "AAEC", "2133": "1"})
	}))
}

func TestSend_474RefreshesClientVersionAndRetries(t *testing.T) {
	cachePath := resetClientVersion(t)
	var seen []string
	srv := tutaStub(t, "999.260101.0", &seen)
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.ServiceRequest("GET", "sys", "SaltService", "SaltData",
		map[string]any{"_format": 0, "mailAddress": "u@x.com"}, "SaltReturn")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(seen) != 2 || seen[0] != consts.ClientVersion || seen[1] != "999.260101.0" {
		t.Fatalf("cv sequence = %v, want [%s 999.260101.0]", seen, consts.ClientVersion)
	}
	b, err := os.ReadFile(cachePath)
	if err != nil || strings.TrimSpace(string(b)) != "999.260101.0" {
		t.Fatalf("cache file = %q, %v", b, err)
	}
}

func TestSend_474RetriesPOSTWithBody(t *testing.T) {
	resetClientVersion(t)
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.js" {
			w.Write([]byte(`"versionNumber":"999.260101.0"`))
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		bodies = append(bodies, string(b))
		if r.Header.Get("cv") != "999.260101.0" {
			w.WriteHeader(StatusInvalidSoftwareVersion)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	_, err := c.ServiceRequest("POST", "sys", "MailAddressAliasService",
		"MailAddressAliasServiceData", map[string]any{
			"_format": 0, "mailAddress": "alias@example.com", "group": "g",
		}, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Fatalf("retry must resend the same body: %q", bodies)
	}
}

func TestSend_474SameVersionDoesNotLoop(t *testing.T) {
	resetClientVersion(t)
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.js" {
			w.Write([]byte(`"versionNumber":"` + consts.ClientVersion + `"`))
			return
		}
		seen = append(seen, r.Header.Get("cv"))
		w.WriteHeader(StatusInvalidSoftwareVersion)
	}))
	defer srv.Close()

	err := NewClient(srv.URL, "").EntityDelete("sys", "Session", "l", "e")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != StatusInvalidSoftwareVersion {
		t.Fatalf("want 474 APIError, got %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("requests = %d, want 1 (no retry with an unchanged version)", len(seen))
	}
	if !strings.Contains(err.Error(), "InvalidSoftwareVersionError") || !strings.Contains(err.Error(), fmt.Sprintf("v=%d", consts.SysModelVersion)) {
		t.Errorf("error should name the error and hint at the model version: %q", err.Error())
	}
}

func TestCurrentClientVersion_IgnoresGarbageCache(t *testing.T) {
	cachePath := resetClientVersion(t)
	os.MkdirAll(filepath.Dir(cachePath), 0o700)
	os.WriteFile(cachePath, []byte("<html>not a version</html>\n"), 0o600)
	if got := currentClientVersion(); got != consts.ClientVersion {
		t.Errorf("currentClientVersion() = %q, want fallback %q", got, consts.ClientVersion)
	}
}

func TestPickClientVersion_NewerWins(t *testing.T) {
	cases := []struct{ cached, builtin, want string }{
		{"", "359.260904.0", "359.260904.0"},
		{"361.261001.0", "359.260904.0", "361.261001.0"},
		{"348.260506.0", "359.260904.0", "359.260904.0"},
		{"359.260904.0", "359.260904.0", "359.260904.0"},
	}
	for _, c := range cases {
		if got := pickClientVersion(c.cached, c.builtin); got != c.want {
			t.Errorf("pickClientVersion(%q, %q) = %q, want %q", c.cached, c.builtin, got, c.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"359.260904.0", "359.260904.0", 0},
		{"348.260506.0", "359.260904.0", -1},
		{"360.260915.2", "359.260904.0", 1},
		{"359.260904.10", "359.260904.9", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestAPIError_ErrorNamesTutaStatus(t *testing.T) {
	cases := []struct {
		err  *APIError
		want string
	}{
		{&APIError{Status: 474}, "HTTP 474 InvalidSoftwareVersionError"},
		{&APIError{Status: 404, Message: "gone"}, "HTTP 404 NotFoundError: gone"},
		{&APIError{Status: 418}, "HTTP 418"},
		{&APIError{Status: 400, Code: "Custom", Message: "m"}, "HTTP 400 Custom: m"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("Error() = %q, want %q", got, c.want)
		}
	}
}

// Cache content must be a bare version; a matching substring is not enough.
func TestCurrentClientVersion_RejectsPaddedCache(t *testing.T) {
	cachePath := resetClientVersion(t)
	os.MkdirAll(filepath.Dir(cachePath), 0o700)
	os.WriteFile(cachePath, []byte(`999.0.0" junk`+"\n"), 0o600)
	if got := currentClientVersion(); got != consts.ClientVersion {
		t.Errorf("currentClientVersion() = %q, want fallback %q", got, consts.ClientVersion)
	}
}

// Transport errors must not print the ?_body= query (it can carry accessToken).
func TestSend_TransportErrorRedactsQuery(t *testing.T) {
	resetClientVersion(t)
	c := NewClient("http://127.0.0.1:1", "")
	_, err := c.ServiceRequest("GET", "sys", "SaltService", "SaltData",
		map[string]any{"_format": 0, "mailAddress": "secret-token@x.com"}, "SaltReturn")
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "_body") {
		t.Errorf("query leaked into error: %v", err)
	}
}

// Parallel requests that all hit 474 with the same stale cv must share one
// refresh and all succeed — none may report a "stale sys model" just because
// another goroutine already refreshed the global cv.
func TestSend_Parallel474ShareOneRefresh(t *testing.T) {
	resetClientVersion(t)
	const n = 6
	const live = "999.260101.0"
	var indexFetches atomic.Int32
	arrived := make(chan struct{}, n)
	release := make(chan struct{})
	var releaseOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.js" {
			indexFetches.Add(1)
			w.Write([]byte(`"versionNumber":"` + live + `"`))
			return
		}
		if r.Header.Get("cv") != live {
			// Hold every stale request until all n have been sent, so each
			// one is rejected before any refresh can finish.
			arrived <- struct{}{}
			if len(arrived) == n {
				releaseOnce.Do(func() { close(release) })
			}
			select {
			case <-release:
			case <-time.After(5 * time.Second):
				t.Error("barrier timeout: not all requests arrived")
			}
			w.Header().Set("error-id", "abc")
			w.WriteHeader(StatusInvalidSoftwareVersion)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = NewClient(srv.URL, "tok").EntityDelete("sys", "Session", "l", fmt.Sprint(i))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("request %d: %v", i, err)
		}
	}
	if got := indexFetches.Load(); got != 1 {
		t.Errorf("index.js fetched %d times, want 1 (concurrent refreshes must merge)", got)
	}
	if got := currentClientVersion(); got != live {
		t.Errorf("cv = %q, want %q", got, live)
	}
}

// A live version that is not newer than the rejected one is no fix: no retry,
// nothing cached, and the error points at the sys model version.
func TestSend_474OlderLiveVersionDoesNotRetryOrCache(t *testing.T) {
	cachePath := resetClientVersion(t)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.js" {
			w.Write([]byte(`"versionNumber":"1.0.0"`))
			return
		}
		requests.Add(1)
		w.Header().Set("error-id", "abc")
		w.WriteHeader(StatusInvalidSoftwareVersion)
	}))
	defer srv.Close()

	err := NewClient(srv.URL, "").EntityDelete("sys", "Session", "l", "e")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != StatusInvalidSoftwareVersion {
		t.Fatalf("want 474 APIError, got %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("requests = %d, want 1 (no retry with an older version)", got)
	}
	if got := currentClientVersion(); got != consts.ClientVersion {
		t.Errorf("active cv = %q, want unchanged %q", got, consts.ClientVersion)
	}
	if _, statErr := os.Stat(cachePath); !os.IsNotExist(statErr) {
		t.Errorf("older version must not be cached: %v", statErr)
	}
	for _, want := range []string{fmt.Sprintf("v=%d", consts.SysModelVersion), "error-id abc"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

// When the refresh itself fails, the original error detail (error-id) stays
// and the refresh failure is appended.
func TestSend_474RefreshFailureKeepsOriginalMessage(t *testing.T) {
	resetClientVersion(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.js" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("error-id", "abc")
		w.WriteHeader(StatusInvalidSoftwareVersion)
	}))
	defer srv.Close()

	err := NewClient(srv.URL, "").EntityDelete("sys", "Session", "l", "e")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"error-id abc", "HTTP 502", consts.ClientVersion} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

// Deterministic form of the race: another request refreshes the global cv
// while this one is in flight with the old value. The 474 must be retried
// with the already-refreshed cv, without a second refresh and without the
// "sys model is stale" error.
func TestSend_474AfterConcurrentRefreshRetriesDirectly(t *testing.T) {
	resetClientVersion(t)
	const live = "999.260101.0"
	var indexFetches atomic.Int32
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.js" {
			indexFetches.Add(1)
			w.Write([]byte(`"versionNumber":"` + live + `"`))
			return
		}
		seen = append(seen, r.Header.Get("cv"))
		if r.Header.Get("cv") != live {
			// Simulate a parallel request finishing its refresh first.
			cvMu.Lock()
			cvValue, cvLoaded = live, true
			cvMu.Unlock()
			w.WriteHeader(StatusInvalidSoftwareVersion)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()

	if err := NewClient(srv.URL, "").EntityDelete("sys", "Session", "l", "e"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(seen) != 2 || seen[0] != consts.ClientVersion || seen[1] != live {
		t.Errorf("cv sequence = %v", seen)
	}
	if got := indexFetches.Load(); got != 0 {
		t.Errorf("index.js fetched %d times, want 0", got)
	}
}

// send recognises the 474 by its status alone, so an error body that stops
// short must not hide it: the retry with the live version still has to happen.
func TestSend_474WithTruncatedBodyStillRetries(t *testing.T) {
	resetClientVersion(t)
	const live = "999.260101.0"
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.js" {
			w.Write([]byte(`"versionNumber":"` + live + `"`))
			return
		}
		seen = append(seen, r.Header.Get("cv"))
		if r.Header.Get("cv") != live {
			w.Header().Set("Content-Length", "500")
			w.WriteHeader(StatusInvalidSoftwareVersion)
			io.WriteString(w, "stale")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := NewClient(srv.URL, "").EntityDelete("sys", "Session", "l", "e"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(seen) != 2 || seen[0] != consts.ClientVersion || seen[1] != live {
		t.Errorf("cv sequence = %v, want [%s %s]", seen, consts.ClientVersion, live)
	}
}
