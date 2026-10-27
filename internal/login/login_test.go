package login

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tdwgm/tuta-alias/internal/api"
)

// Simulate the full successful login flow against a fake server. Real
// Argon2id is run (it's ~50ms with default params at test scale — well
// inside go test default timeout).
func TestLogin_SuccessNoTOTP(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/rest/sys/saltservice":
			json.NewEncoder(w).Encode(map[string]any{
				"421":  "0",
				"422":  "AAEC",
				"2133": "1",
			})
		case "/rest/sys/sessionservice":
			// CreateSessionReturn — challenges empty, user is "userID", accessToken arbitrary.
			// 16-byte raw -> valid for ParseSessionID later (not exercised here).
			json.NewEncoder(w).Encode(map[string]any{
				"1220": "0",
				"1221": "QUJDREVGR0hJSktMTU5PUA",
				"1222": []any{},
				"1223": []any{"userID-XYZ"},
			})
		case "/rest/sys/user/userID-XYZ":
			json.NewEncoder(w).Encode(map[string]any{
				"86":   "userID-XYZ",
				"88":   "0",
				"92":   "0",
				"93":   "1",
				"2132": "1",
				"95": []any{map[string]any{
					"26": "0",
					"28": "1",
					"29": []any{"userGroupID"},
					"30": []any{[]any{"listID", "elemID"}},
				}},
				"96": []any{map[string]any{
					"26":   "0",
					"28":   "0",
					"1030": "5", // GroupTypeMail
					"29":   []any{"mailGroupID"},
					"30":   []any{[]any{"listID", "elemID"}},
				}},
				"99": []any{}, // no customer
			})
		default:
			t.Errorf("unexpected hit: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := api.NewClient(srv.URL, "")
	sess, err := Login(c, "u@example.com", "passphrase", 0, nil)
	if err != nil {
		t.Fatalf("login err: %v", err)
	}
	if sess.MailAddress != "u@example.com" {
		t.Errorf("mailAddress = %q", sess.MailAddress)
	}
	if sess.UserID != "userID-XYZ" {
		t.Errorf("userID = %q", sess.UserID)
	}
	if sess.UserGroupID != "userGroupID" {
		t.Errorf("userGroupID = %q", sess.UserGroupID)
	}
	if sess.MailGroupID != "mailGroupID" {
		t.Errorf("mailGroupID = %q", sess.MailGroupID)
	}
	if len(hits) < 3 {
		t.Errorf("expected ≥3 endpoint hits, got %v", hits)
	}
}

func TestLogin_TOTPRequiredReturnsTotpRequiredError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/sys/saltservice":
			json.NewEncoder(w).Encode(map[string]any{"421": "0", "422": "AAEC", "2133": "1"})
		case "/rest/sys/sessionservice":
			json.NewEncoder(w).Encode(map[string]any{
				"1220": "0",
				"1221": "QUJDREVGR0hJSktMTU5PUA",
				"1222": []any{map[string]any{}}, // non-empty challenges
				"1223": []any{"u"},
			})
		}
	}))
	defer srv.Close()
	c := api.NewClient(srv.URL, "")
	_, err := Login(c, "u@x", "pw", 0, nil)
	if !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("err = %v, want ErrTOTPRequired", err)
	}
}

// An answer that never arrived is not an approval. The pending flag read as
// false when the response was missing, so a poll cut off by the network ended
// the wait as if the user had approved the login.
func TestWaitForSecondFactor(t *testing.T) {
	pending := func(flag string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"1237": "0", "1238": flag})
		}
	}
	cases := []struct {
		name    string
		handler http.HandlerFunc
		wantErr string // "" = approved
	}{
		{"approved", pending("0"), ""},
		{"still pending at the deadline", pending("1"), "timed out"},
		{"response cut off", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "500")
		}, "second-factor poll failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			err := waitForSecondFactor(api.NewClient(srv.URL, ""), "tok", 50*time.Millisecond, 5*time.Millisecond)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("err = %v, want the second factor approved", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want one mentioning %q", err, tc.wantErr)
			}
		})
	}
}

// A reply that does not carry the flag is not a reply that says "no longer
// pending". The flag read as false whenever it was missing, so an empty body, an
// object without it and a null all ended the wait as if the user had approved
// the login. Tuta's type model makes the field required, so none of them comes
// from a Tuta server: each means the protocol changed or the endpoint is not
// Tuta, and the wait has to end with an error that says so, and at once.
func TestWaitForSecondFactor_MissingFlagIsNotApproval(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"object without the flag", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"1237": "0"})
		}},
		{"flag is null", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"1237": "0", "1238": nil})
		}},
		{"empty object", func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "{}")
		}},
		{"another service's reply", func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"status":"ok"}`)
		}},
		{"no body", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var polls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				polls.Add(1)
				tc.handler(w, r)
			}))
			defer srv.Close()

			// A deadline far beyond the test: a wait that kept polling would not end in time.
			err := waitForSecondFactor(api.NewClient(srv.URL, ""), "tok", 2*time.Second, 5*time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), "secondFactorPending") {
				t.Errorf("err = %v, want one saying the reply has no secondFactorPending", err)
			}
			if n := polls.Load(); n != 1 {
				t.Errorf("polled %d times, want 1: a reply like this is not worth waiting out", n)
			}
		})
	}
}

// The same thing from the caller's side. Login with a TOTP code goes on to load
// the account as soon as the wait ends without an error, so a poll that never
// said the login was approved must not end in a session.
func TestLogin_SecondFactorPollWithoutTheFlagFailsTheLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/sys/saltservice":
			json.NewEncoder(w).Encode(map[string]any{"421": "0", "422": "AAEC", "2133": "1"})
		case "/rest/sys/sessionservice":
			json.NewEncoder(w).Encode(map[string]any{
				"1220": "0",
				"1221": "QUJDREVGR0hJSktMTU5PUA",
				"1222": []any{map[string]any{}}, // a challenge: the account has a second factor
				"1223": []any{"userID-XYZ"},
			})
		case "/rest/sys/secondfactorauthservice":
			// The POST takes the code. The GET that follows is the poll, and it
			// answers without saying whether the approval is still pending.
			if r.Method == http.MethodGet {
				json.NewEncoder(w).Encode(map[string]any{"1237": "0"})
			}
		case "/rest/sys/user/userID-XYZ":
			// What a login that wrongly counts the poll as approved goes on to load.
			json.NewEncoder(w).Encode(map[string]any{
				"86": "userID-XYZ",
				"95": []any{map[string]any{
					"26": "0",
					"28": "1",
					"29": []any{"userGroupID"},
					"30": []any{[]any{"listID", "elemID"}},
				}},
				"96": []any{},
				"99": []any{},
			})
		default:
			t.Errorf("unexpected hit: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	sess, err := Login(api.NewClient(srv.URL, ""), "u@example.com", "passphrase", 123456, nil)
	if err == nil || !strings.Contains(err.Error(), "secondFactorPending") {
		t.Errorf("Login = (%v, %v), want an error saying the poll had no secondFactorPending", sess, err)
	}
	if sess != nil {
		t.Errorf("Login returned a session for a second factor that was never confirmed: %+v", sess)
	}
}
