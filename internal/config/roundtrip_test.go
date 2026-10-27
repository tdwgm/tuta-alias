package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A value must survive Save -> Load unchanged. It did not: Doc.Set escaped
// backslashes and quotes, parseKV never unescaped them, and every save added
// another level. A SHOW_ALIASES_FOR regexp stopped matching after two edits.
func TestValuesSurviveRoundTrip(t *testing.T) {
	for _, v := range []string{
		`^spam\.example\.com$`,
		`a"b`,
		`C:\keys\vault.kdbx`,
		`plain value`,
		`\d{3}`,
		``,
	} {
		d := NewDoc()
		d.Set("SHOW_ALIASES_FOR", v)
		again, err := Parse(d.Bytes())
		if err != nil {
			t.Fatalf("parse %q: %v", v, err)
		}
		got, _ := again.Get("SHOW_ALIASES_FOR")
		if got != v {
			t.Errorf("round trip changed %q into %q\nfile: %s", v, got, d.Bytes())
		}
		// And again, because the bug grew one level per save.
		d2 := NewDoc()
		d2.Set("SHOW_ALIASES_FOR", got)
		third, _ := Parse(d2.Bytes())
		if g2, _ := third.Get("SHOW_ALIASES_FOR"); g2 != v {
			t.Errorf("second round trip changed %q into %q", v, g2)
		}
	}
}

// A hand-written value is read as typed: a user writing \. in vim means the
// regexp \., not an escape sequence of this file format.
func TestHandWrittenEscapesAreNotEaten(t *testing.T) {
	d, err := Parse([]byte(`SHOW_ALIASES_FOR="^spam\.example\.com$"` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Get("SHOW_ALIASES_FOR"); got != `^spam\.example\.com$` {
		t.Errorf("hand-written value read as %q", got)
	}
}

// Apply is the one setter: every key must be settable through it, and the
// value must come back out of ToMap.
func TestApplySetsEveryKey(t *testing.T) {
	cfg := Default()
	for _, f := range Fields {
		want := f.Default
		switch {
		case Choices(f.Key) != nil:
			want = Choices(f.Key)[len(Choices(f.Key))-1]
		case want == "":
			continue
		}
		if err := Apply(cfg, f.Key, want); err != nil {
			t.Errorf("Apply(%s, %q): %v", f.Key, want, err)
			continue
		}
		if got := ToMap(cfg)[f.Key]; got != want {
			t.Errorf("Apply(%s, %q) stored %q", f.Key, want, got)
		}
	}
	if err := Apply(cfg, "NOT_A_KEY", "x"); err == nil {
		t.Error("Apply accepted an unknown key")
	}
}

// Every default must pass the validator, or the file the installer writes is
// one `config check` away from being called broken.
func TestDefaultsValidate(t *testing.T) {
	for _, f := range Fields {
		if err := ValidateKey(f.Key, f.Default); err != nil {
			t.Errorf("default for %s (%q) is not valid: %v", f.Key, f.Default, err)
		}
	}
}

func TestValidateKeyRejectsNonsense(t *testing.T) {
	for _, c := range []struct{ key, value string }{
		{"PLUS_SUFFIX_LEN", "abc"},
		{"PLUS_SUFFIX_LEN", "0"},
		{"SERVE_PORT", "70000"},
		{"SERVE_HOST", "not a host"},
		{"SHOW_ALIASES_FOR", "[unclosed"},
		{"CACHE_TTL", "soon"},
		{"TUTA_PROXY", "127.0.0.1:1055"},
		{"CRED_BACKEND", "lastpass"},
		{"COLOR_TEXT", "puce"},
		{"THEME", "grey"}, // invisible as an accent
		{"BANNER_FONT", "comic"},
		{"INSTALL_PATH", "relative/path"},
		{"DEFAULT_DOMAIN", "u@example.com"},
	} {
		if err := ValidateKey(c.key, c.value); err == nil {
			t.Errorf("%s=%q was accepted", c.key, c.value)
		}
	}
	// "default" is the pre-0.12 name for the logo font and still reads.
	if err := ValidateKey("BANNER_FONT", "default"); err != nil {
		t.Errorf("BANNER_FONT=default must still be accepted: %v", err)
	}
	cfg := Default()
	_ = Apply(cfg, "BANNER_FONT", "default")
	if cfg.BannerFont != "logo" {
		t.Errorf("BANNER_FONT=default normalised to %q, want logo", cfg.BannerFont)
	}
}

// Validate reports a bad value and an unknown key without touching the file.
func TestValidateWholeFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "tuta-alias")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("SERVE_PORT=\"70000\"\nWHAT_IS_THIS=\"1\"\nCOLOR_TEXT=\"magenta\"\n")
	doc, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	problems, unknown, _ := Validate(doc)
	if len(problems) != 1 {
		t.Errorf("problems = %v, want 1 (SERVE_PORT)", problems)
	}
	if len(unknown) != 1 || unknown[0] != "WHAT_IS_THIS" {
		t.Errorf("unknown = %v", unknown)
	}
}

// ShortPath must not confuse a sibling directory with $HOME. A plain prefix
// test turns /home/username/x into "~name/x" when home is /home/user, which
// is both wrong and a small leak of the real path. Found by review, not by a
// crash, which is why it is pinned here.
func TestShortPathNeedsASeparator(t *testing.T) {
	home := filepath.Join(t.TempDir(), "user")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	for _, c := range []struct{ in, want string }{
		{filepath.Join(home, ".config", "x"), filepath.Join("~", ".config", "x")},
		{home, "~"},
		{home + "name/x", home + "name/x"}, // a sibling, not a child
		{"/etc/passwd", "/etc/passwd"},
		{"", ""},
	} {
		if got := ShortPath(c.in); got != c.want {
			t.Errorf("ShortPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
