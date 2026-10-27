package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/tdwgm/tuta-alias/internal/config"
)

// COLOR_TEXT was a config key that painted nothing: the screens wrote plain
// strings straight into the output, so body text took the terminal's own
// foreground no matter what the user chose. These tests pin the fix.
//
// lipgloss strips colour under `go test` (it sees no TTY), so the profile has
// to be forced — otherwise the assertions compare two unstyled strings and
// pass for the wrong reason.

func withTrueColor(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// ansiPrefix returns the SGR sequence lipgloss emits before text in a named
// theme colour.
func ansiPrefix(name string) string {
	return strings.Split(lipgloss.NewStyle().Foreground(namedColors[name]).Render("x"), "x")[0]
}

func TestBodyTextFollowsColorText(t *testing.T) {
	withTrueColor(t)
	t.Cleanup(func() { applyStyle(config.Default()) })

	cfg := config.Default()
	applyStyle(cfg)
	if got := txt("hello"); got == "hello" {
		t.Fatalf("body text is not styled at all: %q", got)
	}

	white := txt("hello")
	cfg.ColorText = "magenta"
	applyStyle(cfg)
	magenta := txt("hello")
	if white == magenta {
		t.Error("COLOR_TEXT does not change body text — the key paints nothing")
	}
	if !strings.HasPrefix(magenta, ansiPrefix("magenta")) {
		t.Errorf("COLOR_TEXT=magenta produced %q, not the magenta sequence", magenta)
	}
}

// The screens must route body text through txt(). The alias address is the
// most-read text on Home, so it is the canary for a plain WriteString
// sneaking back in.
func TestHomeAliasRowUsesColorText(t *testing.T) {
	withTrueColor(t)
	t.Cleanup(func() { applyStyle(config.Default()) })

	cfg := config.Default()
	cfg.ColorText = "magenta"
	applyStyle(cfg)

	m := homeWithRows(t, "alpha@example.com")
	m.width = 100
	m.listAll = true
	out := m.viewHome()

	if !strings.Contains(out, ansiPrefix("magenta")+"alpha@example.com") {
		t.Errorf("the alias row is not in COLOR_TEXT — still plain terminal text.\n"+
			"wanted the magenta sequence before the address, got:\n%s", out)
	}
}

// Reset restores the whole Theme group and leaves everything else alone —
// the contract of the "[Reset to default]" row.
func TestResetThemeRestoresOnlyTheme(t *testing.T) {
	cfg := config.Default()
	cfg.ColorText = "magenta"
	cfg.ColorAccent = "green"
	cfg.BannerFont = "slant"
	cfg.MenuEffect = "none"
	cfg.DefaultDomain = "example.com" // not a theme key
	cfg.ServePort = "9999"            // not a theme key

	config.ResetTheme(cfg)

	def := config.Default()
	for _, c := range []struct{ name, got, want string }{
		{"COLOR_TEXT", cfg.ColorText, def.ColorText},
		{"THEME", cfg.ColorAccent, def.ColorAccent},
		{"BANNER_FONT", cfg.BannerFont, def.BannerFont},
		{"MENU_EFFECT", cfg.MenuEffect, def.MenuEffect},
	} {
		if c.got != c.want {
			t.Errorf("%s not reset: got %q, want %q", c.name, c.got, c.want)
		}
	}
	if cfg.DefaultDomain != "example.com" {
		t.Errorf("ResetTheme touched DEFAULT_DOMAIN: %q", cfg.DefaultDomain)
	}
	if cfg.ServePort != "9999" {
		t.Errorf("ResetTheme touched SERVE_PORT: %q", cfg.ServePort)
	}
}

// Every key the Config screen can hold must exist in the config file, and
// vice versa — the standard's B6 rule, and what keeps a new theme key from
// being added in one place only.
func TestThemeKeysRoundTripThroughTheFile(t *testing.T) {
	cfg := config.Default()
	m := config.ToMap(cfg)
	for _, f := range config.Fields {
		if _, ok := m[f.Key]; !ok {
			t.Errorf("%s is in Fields but never written to the config file", f.Key)
		}
	}
	for k := range m {
		var found bool
		for _, f := range config.Fields {
			if f.Key == k {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is written to the config file but is not in Fields", k)
		}
	}
}
