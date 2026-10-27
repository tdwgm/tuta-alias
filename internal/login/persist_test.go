package login

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// createSession must send a fresh 32-byte accessKey (attribute 1216), which
// makes the session persistent; two logins must not share a key.
func TestCreateSession_SendsRandomAccessKey(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("body %q: %v", raw, err)
		}
		k, _ := body["1216"].(string)
		keys = append(keys, k)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"1220": "0", "1221": "QUJDREVGR0hJSktMTU5PUA", "1222": []any{}, "1223": []any{"u"},
		})
	}))
	defer srv.Close()

	for i := 0; i < 2; i++ {
		if _, err := createSession(api.NewClient(srv.URL, ""), "u@example.com", "v"); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range keys {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(b) != 32 {
			t.Fatalf("accessKey %q: %d bytes, %v — want 32", k, len(b), err)
		}
	}
	if keys[0] == keys[1] {
		t.Error("two sessions got the same accessKey")
	}
}

func withSessionDir(t *testing.T) *[]string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	closed := &[]string{}
	o := closeSession
	t.Cleanup(func() { closeSession = o })
	closeSession = func(tok string) error { *closed = append(*closed, tok); return nil }
	return closed
}

// ReplaceSession is SaveSession's local half: it names the session it replaced
// and leaves closing it to the caller, so a caller with an event loop can send
// that request from somewhere it may wait.
func TestReplaceSession_NamesTheReplacedTokenAndClosesNothing(t *testing.T) {
	closed := withSessionDir(t)

	p, replaced, err := ReplaceSession(&session.Session{AccessToken: "old"})
	if err != nil || replaced != "" || p != session.Path() {
		t.Fatalf("first save: path=%q replaced=%q err=%v, want nothing replaced", p, replaced, err)
	}
	if _, replaced, _ = ReplaceSession(&session.Session{AccessToken: "old"}); replaced != "" {
		t.Errorf("saving the same token again replaced %q", replaced)
	}
	if _, replaced, err = ReplaceSession(&session.Session{AccessToken: "new"}); err != nil || replaced != "old" {
		t.Fatalf("replaced = %q, err = %v, want old", replaced, err)
	}
	if len(*closed) != 0 {
		t.Errorf("ReplaceSession closed %v itself", *closed)
	}
	if s, err := session.Load(); err != nil || s.AccessToken != "new" {
		t.Errorf("saved session = %+v, %v", s, err)
	}
}

// A save that fails names nothing to close: the old session is still the
// current one, and closing it would log the user out of a login that failed.
func TestReplaceSession_FailedSaveNamesNothing(t *testing.T) {
	withSessionDir(t)
	if _, err := session.Save(&session.Session{AccessToken: "old"}); err != nil {
		t.Fatal(err)
	}
	// A channel in Extra cannot be marshalled, so the save fails after the old
	// file was read. Permissions would not do: the tests run as root too.
	bad := &session.Session{AccessToken: "new", Extra: map[string]any{"x": make(chan int)}}
	if _, replaced, err := ReplaceSession(bad); err == nil || replaced != "" {
		t.Errorf("replaced = %q, err = %v, want an error and no token", replaced, err)
	}
	if s, err := session.Load(); err != nil || s.AccessToken != "old" {
		t.Errorf("a failed save disturbed the session file: %+v, %v", s, err)
	}
}

// A re-login closes the session it replaces — persistent sessions never
// expire, so they would otherwise pile up server-side.
func TestSaveSession_ClosesReplacedSession(t *testing.T) {
	closed := withSessionDir(t)
	if _, err := SaveSession(&session.Session{AccessToken: "old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveSession(&session.Session{AccessToken: "old"}); err != nil {
		t.Fatal(err)
	}
	if len(*closed) != 0 {
		t.Fatalf("closed %v; saving the same token again must not close it", *closed)
	}
	if _, err := SaveSession(&session.Session{AccessToken: "new"}); err != nil {
		t.Fatal(err)
	}
	if len(*closed) != 1 || (*closed)[0] != "old" {
		t.Fatalf("closed %v, want [old]", *closed)
	}
	if s, err := session.Load(); err != nil || s.AccessToken != "new" {
		t.Fatalf("saved session = %+v, %v", s, err)
	}
}

// ClearSession is Logout's local half: the file is gone when it returns, the
// token it held comes back for the caller to close, and nothing is sent.
func TestClearSession_ReturnsTheTokenAndClosesNothing(t *testing.T) {
	closed := withSessionDir(t)
	if token, cleared, err := ClearSession(); token != "" || cleared || err != nil {
		t.Fatalf("no session: token=%q cleared=%v err=%v", token, cleared, err)
	}
	if _, err := session.Save(&session.Session{AccessToken: "tok"}); err != nil {
		t.Fatal(err)
	}
	token, cleared, err := ClearSession()
	if token != "tok" || !cleared || err != nil {
		t.Fatalf("token=%q cleared=%v err=%v, want tok, true, nil", token, cleared, err)
	}
	if len(*closed) != 0 {
		t.Errorf("ClearSession closed %v itself", *closed)
	}
	if _, err := session.Load(); err == nil {
		t.Error("session file still present after ClearSession")
	}
}

// The wrap is the same one Logout makes, because they share it: a caller that
// clears locally must still be able to tell a file that stayed from a server
// close that failed.
func TestClearSession_ReportsASessionFileItCouldNotDelete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	stuck := session.Path()
	if err := os.MkdirAll(filepath.Join(stuck, "in-the-way"), 0o700); err != nil {
		t.Fatal(err)
	}

	_, cleared, err := ClearSession()
	if cleared {
		t.Error("ClearSession() said it cleared a session it could not delete")
	}
	if !errors.Is(err, ErrSessionFileRemains) {
		t.Fatalf("err = %v, want one wrapping ErrSessionFileRemains", err)
	}
	if _, statErr := os.Stat(stuck); statErr != nil {
		t.Errorf("the session path is gone after all: %v", statErr)
	}
}

func TestLogout_ClosesAndClears(t *testing.T) {
	closed := withSessionDir(t)
	if cleared, err := Logout(); cleared || err != nil || len(*closed) != 0 {
		t.Fatalf("no session: cleared=%v err=%v closed=%v", cleared, err, *closed)
	}
	if _, err := session.Save(&session.Session{AccessToken: "tok"}); err != nil {
		t.Fatal(err)
	}
	cleared, err := Logout()
	if !cleared || err != nil || len(*closed) != 1 || (*closed)[0] != "tok" {
		t.Fatalf("cleared=%v err=%v closed=%v", cleared, err, *closed)
	}
	if _, err := session.Load(); err == nil {
		t.Error("session file still present after Logout")
	}
}

// Closing an already-dead session is not an error.
func TestCloseSession_AlreadyGone(t *testing.T) {
	for _, status := range []int{401, 404, 440} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "DELETE" || !strings.HasPrefix(r.URL.Path, "/rest/sys/session/") {
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(status)
		}))
		err := closeSessionAt(srv.URL, "QUJDREVGR0hJSktMTU5PUA")
		srv.Close()
		if err != nil {
			t.Errorf("status %d: %v", status, err)
		}
	}
}

// A logout that cannot delete the session file is a failure, not a warning:
// the access token is still on disk and the next command logs back in with
// it. session.Clear's error used to be dropped, so `tuta-alias logout`
// printed "No session to clear." and exited 0 in exactly that case.
func TestLogoutReportsASessionFileItCouldNotDelete(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", cache)

	// A directory where the session file belongs: os.Remove refuses to
	// delete a non-empty one, whoever is running the test (root included).
	stuck := session.Path()
	if err := os.MkdirAll(filepath.Join(stuck, "in-the-way"), 0o700); err != nil {
		t.Fatal(err)
	}

	cleared, err := Logout()
	if cleared {
		t.Error("Logout() said it cleared a session it could not delete")
	}
	if !errors.Is(err, ErrSessionFileRemains) {
		t.Fatalf("err = %v, want one wrapping ErrSessionFileRemains", err)
	}
	if _, statErr := os.Stat(stuck); statErr != nil {
		t.Errorf("the session path is gone after all: %v", statErr)
	}
}

// With nothing to log out of, the answer is still "nothing to clear" and no
// error at all.
func TestLogoutWithNoSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cleared, err := Logout()
	if cleared || err != nil {
		t.Errorf("Logout() = (%v, %v), want (false, nil)", cleared, err)
	}
}
