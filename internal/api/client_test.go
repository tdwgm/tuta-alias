package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServiceRequest_GETWithBody_EncodesBodyAsQueryParam(t *testing.T) {
	var gotPath, gotQuery, gotV, gotCV, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("_body")
		gotV = r.Header.Get("v")
		gotCV = r.Header.Get("cv")
		gotToken = r.Header.Get("accessToken")
		// Return a SaltReturn-shaped raw response.
		json.NewEncoder(w).Encode(map[string]any{
			"421":  "0",
			"422":  "AAEC", // base64 for [0,1,2]
			"2133": "1",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok123")
	out, err := c.ServiceRequest("GET", "sys", "SaltService", "SaltData",
		map[string]any{"_format": 0, "mailAddress": "u@x.com"}, "SaltReturn")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if gotPath != "/rest/sys/saltservice" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "\"419\"") {
		t.Errorf("_body did not include packed mailAddress field id 419: %q", gotQuery)
	}
	if gotV == "" || gotCV == "" {
		t.Errorf("missing v/cv headers: v=%q cv=%q", gotV, gotCV)
	}
	if gotToken != "tok123" {
		t.Errorf("accessToken header = %q", gotToken)
	}
	m, _ := out.(map[string]any)
	if kdf, _ := m["kdfVersion"].(int); kdf != 1 {
		t.Errorf("kdfVersion: %v", m["kdfVersion"])
	}
}

func TestServiceRequest_POST_SendsJSONBody(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok")
	_, err := c.ServiceRequest("POST", "sys", "MailAddressAliasService",
		"MailAddressAliasServiceData", map[string]any{
			"_format":     0,
			"mailAddress": "alias@example.com",
			"group":       "groupID",
		}, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(gotBody, "\"689\"") || !strings.Contains(gotBody, "alias@example.com") {
		t.Errorf("body missing packed fields: %s", gotBody)
	}
}

func TestServiceRequest_Error_Redacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Error", "NotAuthenticatedError")
		w.WriteHeader(401)
		w.Write([]byte(`{"authVerifier":"abcdef==","note":"shouldStay"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok")
	_, err := c.ServiceRequest("GET", "sys", "SaltService", "SaltData",
		map[string]any{"_format": 0, "mailAddress": "x@y.com"}, "SaltReturn")
	if err == nil {
		t.Fatalf("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err type = %T", err)
	}
	if apiErr.Status != 401 {
		t.Errorf("status = %d", apiErr.Status)
	}
	if strings.Contains(apiErr.Message, "abcdef==") {
		t.Errorf("authVerifier not redacted: %q", apiErr.Message)
	}
	if !strings.Contains(apiErr.Message, "shouldStay") {
		t.Errorf("unrelated content removed: %q", apiErr.Message)
	}
}

// The error a failed request returns is printed as it is, to stderr by the CLI
// and into the error line by the TUI, and a terminal runs an escape sequence
// instead of showing it. Besides the body, the Error header and the error-id
// header end up in the message. A header cannot carry ESC or a newline, but it
// can carry a C1 control (U+009B is CSI, U+009D is OSC).
func TestServiceRequest_ErrorTextCannotReachTheTerminal(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		visible string // how the hostile part has to read once escaped
	}{
		{"the body", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
			io.WriteString(w, "upstream failure\n2026-10-27 12:00:00 INFO forged\x1b]52;c;ZXZpbA==\x07")
		}, `failure\n2026-10-27 12:00:00 INFO forged\x1b]52;c;ZXZpbA==\x07`},
		{"the Error header", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Error", "Bad\u009d52;c;ZXZpbA==\u009cError")
			w.WriteHeader(500)
		}, `Bad\x9d52;c;ZXZpbA==\x9cError`},
		{"the error-id header", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("error-id", "abc\u009b2Jdef")
			w.WriteHeader(500)
		}, `error-id abc\x9b2Jdef`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			_, err := NewClient(srv.URL, "tok").EntityLoad("sys", "User", "User", "x")
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v (%T), want an *APIError", err, err)
			}
			got := err.Error()
			if ctl := controlIn(got); ctl != -1 {
				t.Errorf("the error holds the control character %U: %q", ctl, got)
			}
			if !strings.Contains(got, tc.visible) {
				t.Errorf("the escaped text should still be readable as %q: %q", tc.visible, got)
			}
		})
	}
}

// A 2xx response that ends before its Content-Length is a failed request. Its
// body used to be read with the error thrown away, so the caller got "no body,
// no error" and took it for an answer: a DNS check that came back OK, a quota
// of zero, a second factor that was no longer pending.
func TestServiceRequest_TruncatedBodyIsAnError(t *testing.T) {
	stall := make(chan struct{})
	t.Cleanup(func() { close(stall) })
	cases := []struct {
		name    string
		handler http.HandlerFunc
		timeout time.Duration // 0 keeps the client's own
		want    error
	}{
		{"cut off before the first byte", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "500")
		}, 0, io.ErrUnexpectedEOF},
		{"cut off inside the JSON", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "500")
			io.WriteString(w, `{"1590":"0","1591":"0"`)
		}, 0, io.ErrUnexpectedEOF},
		{"stalled until Client.Timeout", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "500")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-stall:
			case <-r.Context().Done():
			}
		}, 100 * time.Millisecond, context.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			c := NewClient(srv.URL, "tok")
			if tc.timeout > 0 {
				c.HTTP.Timeout = tc.timeout
			}

			_, err := c.ServiceRequest("GET", "sys", "CustomDomainCheckService", "CustomDomainCheckGetIn",
				map[string]any{"_format": 0, "domain": "example.com"}, "CustomDomainCheckGetOut")
			if !errors.Is(err, tc.want) {
				t.Errorf("ServiceRequest err = %v, want one wrapping %v", err, tc.want)
			}
			if _, err := c.EntityLoad("sys", "CustomerInfo", "CustomerInfo", "l", "e"); !errors.Is(err, tc.want) {
				t.Errorf("EntityLoad err = %v, want one wrapping %v", err, tc.want)
			}
		})
	}
}

func TestEntityLoad_BuildsPathFromIDs(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{"86": "userID"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok")
	_, err := c.EntityLoad("sys", "User", "User", "userID-XYZ")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if gotPath != "/rest/sys/user/userID-XYZ" {
		t.Errorf("path = %q", gotPath)
	}
}
