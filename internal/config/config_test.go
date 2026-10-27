package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withTempHome(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("HOME", d)
	return d
}

func TestPath_UnderHomeConfig(t *testing.T) {
	home := withTempHome(t)
	want := filepath.Join(home, ".config", "tuta-alias", "tuta-alias.conf")
	if got := Path(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestLoad_Missing_ReturnsDefaults(t *testing.T) {
	withTempHome(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.TUIEnabled {
		t.Errorf("TUIEnabled default should be true")
	}
	if c.CacheTTL != "8h" {
		t.Errorf("CacheTTL = %q, want 8h", c.CacheTTL)
	}
	if c.ServePort != "8088" {
		t.Errorf("ServePort = %q, want 8088", c.ServePort)
	}
	if !c.CompletionBash || !c.CompletionZsh {
		t.Errorf("completion defaults should be true")
	}
	if c.PlusSuffixLen != "5" {
		t.Errorf("PlusSuffixLen = %q, want 5", c.PlusSuffixLen)
	}
	if c.PlusSuffixLenInt() != 5 {
		t.Errorf("PlusSuffixLenInt() = %d, want 5", c.PlusSuffixLenInt())
	}
	if c.TutaProxy != "" {
		t.Errorf("TutaProxy = %q, want empty (direct)", c.TutaProxy)
	}
}

func TestPlusSuffixLenInt_Fallback(t *testing.T) {
	cases := map[string]int{"": 5, "abc": 5, "0": 5, "-3": 5, "8": 8, " 12 ": 12}
	for in, want := range cases {
		c := &Config{PlusSuffixLen: in}
		if got := c.PlusSuffixLenInt(); got != want {
			t.Errorf("PlusSuffixLenInt(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	withTempHome(t)
	in := &Config{
		DefaultDomain:  "example.com",
		TUIEnabled:     false,
		PlusSuffixLen:  "8",
		ShowQuota:      true,
		ShowAliasesFor: "example.com",
		CredBackend:    "keepassxc",
		KeePassDB:      "/srv/secrets/vault.kdbx",
		KeePassEntry:   "Tuta/main",
		CacheTTL:       "reboot",
		TutaProxy:      "socks5://127.0.0.1:1055",
		ServeHost:      "127.0.0.1",
		ServePort:      "9090",
		CompletionBash: true,
		CompletionZsh:  false,
	}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.DefaultDomain != in.DefaultDomain ||
		out.TUIEnabled != in.TUIEnabled ||
		out.PlusSuffixLen != in.PlusSuffixLen ||
		out.ShowQuota != in.ShowQuota ||
		out.ShowAliasesFor != in.ShowAliasesFor ||
		out.CredBackend != in.CredBackend ||
		out.KeePassDB != in.KeePassDB ||
		out.KeePassEntry != in.KeePassEntry ||
		out.CacheTTL != in.CacheTTL ||
		out.TutaProxy != in.TutaProxy ||
		out.ServePort != in.ServePort ||
		out.CompletionBash != in.CompletionBash ||
		out.CompletionZsh != in.CompletionZsh {
		t.Fatalf("round-trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
}

func TestSave_PreservesUserComments(t *testing.T) {
	withTempHome(t)
	custom := []byte(`# My personal note: do not delete.
# example.com is my homemail tld.
DEFAULT_DOMAIN="example.com"

CRED_BACKEND="bitwarden"
BW_ITEM="tuta"
`)
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, custom, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c.DefaultDomain = "example.org"
	if err := Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, _ := os.ReadFile(p)
	body := string(raw)
	for _, want := range []string{
		"My personal note: do not delete.",
		"example.com is my homemail tld.",
		`DEFAULT_DOMAIN="example.org"`,
		`BW_ITEM="tuta"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in saved file:\n%s", want, body)
		}
	}
}

func TestSave_AppendsMissingKeys(t *testing.T) {
	withTempHome(t)
	p := Path()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(`DEFAULT_DOMAIN="x.com"`+"\n"), 0o600)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, _ := os.ReadFile(p)
	body := string(raw)
	for _, key := range []string{"TUI_ENABLED", "CRED_BACKEND", "CACHE_TTL", "TUTA_PROXY", "SERVE_PORT", "COMPLETION_BASH"} {
		if !strings.Contains(body, key+"=") {
			t.Errorf("missing key %s in saved file:\n%s", key, body)
		}
	}
}

func TestParse_ValueWithoutQuotes(t *testing.T) {
	raw := []byte(`SERVE_PORT=8088
DEFAULT_DOMAIN=example.com
`)
	d, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Get("SERVE_PORT"); v != "8088" {
		t.Errorf("SERVE_PORT = %q", v)
	}
	if v, _ := d.Get("DEFAULT_DOMAIN"); v != "example.com" {
		t.Errorf("DEFAULT_DOMAIN = %q", v)
	}
}

func TestDoc_SetPreservesPositionAndComment(t *testing.T) {
	raw := []byte(`# leading comment
KEY_A="a"
# between
KEY_B="b"
# trailing
`)
	d, _ := Parse(raw)
	d.Set("KEY_A", "alpha")
	out := string(d.Bytes())
	if !strings.Contains(out, `KEY_A="alpha"`) {
		t.Errorf("KEY_A not updated: %s", out)
	}
	if !strings.Contains(out, "leading comment") || !strings.Contains(out, "between") || !strings.Contains(out, "trailing") {
		t.Errorf("comments lost: %s", out)
	}
	if strings.Index(out, "leading") > strings.Index(out, "KEY_A") {
		t.Errorf("comment order broken: %s", out)
	}
}

// A fresh config file lists TUTA_PROXY under its own "Network" section header.
func TestSave_TemplateHasNetworkSection(t *testing.T) {
	withTempHome(t)
	if err := Save(Default()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(Path())
	body := string(raw)
	sec, key := strings.Index(body, "# Network\n"), strings.Index(body, `TUTA_PROXY=""`)
	if sec < 0 || key < 0 || sec > key {
		t.Errorf("want a Network header before TUTA_PROXY:\n%s", body)
	}
}

// INSTALL_PATH is acted on: install writes the binary there and uninstall
// looks for it there. A relative value (a hand-edited "~/bin/tuta-alias",
// with no shell to expand the tilde) was used verbatim, so install created
// $PWD/~/bin/tuta-alias while reporting the path as typed, and uninstall run
// from another directory looked somewhere else again. config set refuses such
// a value and config check reports it; BinaryPath must not act on it either.
func TestBinaryPathIgnoresARelativeInstallPath(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
	}{
		{"/opt/tools/tuta-alias", "/opt/tools/tuta-alias"},
		{"", DefaultInstallPath()},
		{"   ", DefaultInstallPath()},
		{"~/bin/tuta-alias", DefaultInstallPath()},
		{"bin/tuta-alias", DefaultInstallPath()},
		{"./tuta-alias", DefaultInstallPath()},
	} {
		cfg := &Config{InstallPath: c.in}
		if got := cfg.BinaryPath(); got != c.want {
			t.Errorf("InstallPath %q -> BinaryPath %q, want %q", c.in, got, c.want)
		}
	}
}

// Choice values are compared exactly by every consumer, so they are stored
// the way the choice list spells them whatever the file or the command said.
func TestChoiceValuesAreCanonicalised(t *testing.T) {
	cases := []struct{ key, in, want string }{
		{"CRED_BACKEND", "BitWarden", "bitwarden"},
		{"CRED_BACKEND", "KEYRING", "keyring"},
		{"CRED_BACKEND", "Vault", "vault"},
		{"LOG_LEVEL", "DEBUG", "debug"},
		{"AUTO_UPDATE", "Check", "check"},
		{"THEME", "GREEN", "green"},
		{"CACHE_TTL", "Reboot", "reboot"},
		// Not a choice key: left exactly as typed.
		{"DEFAULT_DOMAIN", "Example.COM", "Example.COM"},
	}
	for _, c := range cases {
		cfg := Default()
		if err := Apply(cfg, c.key, c.in); err != nil {
			t.Fatalf("Apply(%s, %q): %v", c.key, c.in, err)
		}
		if got := ToMap(cfg)[c.key]; got != c.want {
			t.Errorf("Apply(%s, %q) stored %q, want %q", c.key, c.in, got, c.want)
		}
	}
}

// The same holds for a file written by hand, which is where a capital letter
// is most likely to come from.
func TestLoadCanonicalisesChoiceValues(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("CRED_BACKEND=\"BitWarden\"\nLOG_LEVEL=\"INFO\"\n")
	if err := os.WriteFile(Path(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CredBackend != "bitwarden" {
		t.Errorf("CredBackend = %q, want bitwarden", cfg.CredBackend)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
}
