package services

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/session"
)

func mockSess() *session.Session {
	return &session.Session{
		MailAddress: "u@x", UserID: "U", AccessToken: "tok",
		UserGroupID: "userGroupID", MailGroupID: "mailGroupID",
		GroupInfoID:    []string{"giL", "giE"},
		CustomerInfoID: []string{"ciL", "ciE"},
		Version:        1,
	}
}

func TestAddAlias_SendsUserGroupID_NotMailGroupID(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := api.NewClient(srv.URL, "tok")
	if err := AddAlias(c, mockSess(), "alias@example.com"); err != nil {
		t.Fatalf("err: %v", err)
	}
	// field 691 is MailAddressAliasServiceData.group
	if !strings.Contains(gotBody, `"691":["userGroupID"]`) {
		t.Errorf("group must be UserGroupID. body=%s", gotBody)
	}
	if strings.Contains(gotBody, "mailGroupID") {
		t.Errorf("body must not contain mailGroupID: %s", gotBody)
	}
}

func TestRemoveAlias_RestoreFlag(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := api.NewClient(srv.URL, "tok")
	if err := RemoveAlias(c, mockSess(), "alias@example.com", true); err != nil {
		t.Fatalf("err: %v", err)
	}
	// field 788 = restore (Boolean → "1")
	if !strings.Contains(gotBody, `"788":"1"`) {
		t.Errorf("restore flag missing/wrong: %s", gotBody)
	}
}

func TestListDomains_WalksDomainInfos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"150": "ciE",
			"152": "0",
			"154": "tuta.com",
			"726": []any{
				map[string]any{"697": "x", "698": "example.com"},
				map[string]any{"697": "y", "698": "example.org"},
			},
		})
	}))
	defer srv.Close()
	c := api.NewClient(srv.URL, "tok")
	doms, err := ListDomains(c, mockSess())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(doms) != 2 || doms[0] != "example.com" || doms[1] != "example.org" {
		t.Errorf("doms = %v", doms)
	}
}

// A response that never arrived must not read as an empty answer: a cut-off
// reply decoded as "DNS check OK, no records", a quota of zero, and an account
// with no addresses and no domains.
func TestReadsFailWhenTheResponseIsCutOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "500")
	}))
	defer srv.Close()
	c := api.NewClient(srv.URL, "tok")

	reads := []struct {
		name string
		call func() error
	}{
		{"ListDomains", func() error { _, err := ListDomains(c, mockSess()); return err }},
		{"ListAddresses", func() error { _, _, err := ListAddresses(c, mockSess()); return err }},
		{"AliasQuota", func() error { _, err := AliasQuota(c, mockSess()); return err }},
		{"CheckDomainDNS", func() error { _, err := CheckDomainDNS(c, "example.com"); return err }},
	}
	for _, tc := range reads {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("err = %v, want one wrapping io.ErrUnexpectedEOF", err)
			}
		})
	}
}

func TestSuggestAlias_Format(t *testing.T) {
	addr, err := SuggestAlias("shop", "example.com")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.HasPrefix(addr, "shop-") || !strings.HasSuffix(addr, "@example.com") {
		t.Errorf("addr = %q", addr)
	}
	if len(addr) != len("shop-")+8+len("@example.com") {
		t.Errorf("expected 8 hex chars between - and @: %q", addr)
	}
}

func TestDomainValidationRecord(t *testing.T) {
	// sha256("example.com" + "cust123")[:16], cross-checked with Python hashlib.
	got := DomainValidationRecord("  Example.COM ", "cust123")
	want := "t-verify=" + "a1dd7d803854515ba9a0196c98c1a9cc"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
