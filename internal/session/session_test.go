package session

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRoundTrip_PythonGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/python_session.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	s, err := FromJSON(raw)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if s.MailAddress == "" || s.UserID == "" || s.AccessToken == "" {
		t.Fatalf("missing core fields: %+v", s)
	}
	if len(s.GroupInfoID) != 2 {
		t.Fatalf("group_info_id should be [listId, elementId]: %v", s.GroupInfoID)
	}
	if s.MailAddress != "user@tuta.com" {
		t.Errorf("mail_address: got %q", s.MailAddress)
	}
	if s.AccessToken != "FAKE_TOKEN" {
		t.Errorf("access_token: got %q", s.AccessToken)
	}
	if len(s.CustomerInfoID) != 2 {
		t.Errorf("customer_info_id: got %v", s.CustomerInfoID)
	}

	out, err := s.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	s2, err := FromJSON(out)
	if err != nil {
		t.Fatalf("FromJSON(round-trip): %v", err)
	}
	if !reflect.DeepEqual(s, s2) {
		t.Fatalf("round-trip drift:\n  a=%+v\n  b=%+v", s, s2)
	}
}

func TestSaveLoad_AtomicAndReadOnly(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	s := &Session{
		MailAddress: "u@x.com",
		UserID:      "uid",
		AccessToken: "tok",
		UserGroupID: "ug",
		GroupInfoID: []string{"lid", "eid"},
		SavedAt:     "2026-05-11T00:00:00+00:00",
		Version:     1,
	}
	p, err := Save(s)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o, want 0600", mode)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.MailAddress != s.MailAddress {
		t.Fatalf("mailAddress: got %q want %q", got.MailAddress, s.MailAddress)
	}
	di, _ := os.Stat(filepath.Dir(p))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 0700", di.Mode().Perm())
	}
}

func TestClear_RemovesFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	s := &Session{MailAddress: "u@x.com", UserID: "u", AccessToken: "t",
		UserGroupID: "g", GroupInfoID: []string{"l", "e"}, Version: 1}
	if _, err := Save(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	cleared, err := Clear()
	if err != nil || !cleared {
		t.Fatalf("clear: cleared=%v err=%v", cleared, err)
	}
	if _, err := Load(); err == nil {
		t.Fatalf("expected Load to fail after Clear")
	}
}

func TestClear_NoFileReturnsFalse(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	cleared, err := Clear()
	if err != nil {
		t.Fatalf("clear err: %v", err)
	}
	if cleared {
		t.Fatalf("clear returned true on missing file")
	}
}

// With no HOME and no XDG_CACHE_HOME there is nowhere to keep the session.
// os.UserHomeDir's error used to be discarded, and filepath.Join("", ".cache",
// …) is a RELATIVE path: `tuta-alias whoami` looked for
// ".cache/tuta-alias/session.json" under whatever directory it ran in, and a
// login wrote the access token there, outside the user's home and outside
// everything uninstall --purge removes.
func TestNoHomeMeansNoPathAndNoWritesToTheWorkingDirectory(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	// os.UserHomeDir reads $HOME on unix; an empty value is an error there.
	cwd := t.TempDir()
	t.Chdir(cwd)

	if p := Path(); p != "" {
		t.Errorf("Path() = %q, want \"\" when there is no home directory", p)
	}
	if _, err := Load(); err == nil {
		t.Error("Load() succeeded with no home directory")
	}
	if _, err := Save(&Session{MailAddress: "u@example.com", AccessToken: "tok"}); err == nil {
		t.Error("Save() succeeded with no home directory")
	}
	if cleared, err := Clear(); cleared || err != nil {
		t.Errorf("Clear() = (%v, %v), want (false, nil)", cleared, err)
	}

	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the working directory now holds %v", names)
	}
}

// A relative XDG_CACHE_HOME is invalid per the XDG spec and must be ignored,
// for the same reason: it would put the token wherever the command ran.
func TestRelativeXDGCacheHomeIsIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "relative/cache")

	p := Path()
	if !filepath.IsAbs(p) {
		t.Fatalf("Path() = %q, want an absolute path", p)
	}
	if !strings.HasPrefix(p, home) {
		t.Errorf("Path() = %q, want it under %q", p, home)
	}
}
