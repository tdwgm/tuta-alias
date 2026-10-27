package tui

import (
	"strings"
	"testing"

	"github.com/tdwgm/tuta-alias/internal/config"
)

// theme_std.go is the TUI Scaffold standard's theme group, generated into
// every project that implements the standard. It was dead code: nothing
// referenced it, the test its own header names (standard_parity_test.go) did
// not exist, and the project's field table had drifted from it without a word
// — different accent key, three missing keys, a different BANNER_FONT default
// and a missing colour name.
//
// This file is that test. A difference is allowed, but only as an entry in
// themeDeviations with a reason, so the drift is a decision someone made
// rather than something nobody noticed.

// themeDeviations are the standard keys this application deliberately does
// not implement, and why. It is empty: COLOR_RULE_FOOTER, COLOR_ICON and
// COLOR_BRACKET used to be listed here as "nothing draws them", which was
// true only because nothing had been wired to them.
var themeDeviations = map[string]string{}

// Every standard key is either a config key here or a documented deviation.
func TestStdThemeKeysAreImplementedOrDocumented(t *testing.T) {
	for _, f := range ThemeStd {
		if config.Known(f.Key) {
			if themeDeviations[f.Key] != "" {
				t.Errorf("%s is implemented AND listed as a deviation — drop the entry", f.Key)
			}
			continue
		}
		if themeDeviations[f.Key] == "" {
			t.Errorf("%s is part of the TUI Scaffold theme standard but is not a config key here, "+
				"and there is no entry in themeDeviations saying why", f.Key)
		}
	}
	for key := range themeDeviations {
		if _, ok := ThemeStdLookup(key); !ok {
			t.Errorf("themeDeviations mentions %q, which is not a standard key — stale entry", key)
		}
	}
}

// An implemented key must carry the standard's default, or two tools built on
// the same standard look different out of the box. The two keys the standard
// marks AppDefault are the application's own.
func TestStdThemeDefaultsMatchStandard(t *testing.T) {
	for _, f := range ThemeStd {
		if f.AppDefault || !config.Known(f.Key) {
			continue
		}
		ours, _ := config.Lookup(f.Key)
		if ours.Default != f.Default {
			t.Errorf("%s defaults to %q here, the standard says %q", f.Key, ours.Default, f.Default)
		}
	}
}

// The colour vocabulary must match: a config written for one tool has to read
// in another. Ours may be a superset (ocean is an accent here), never smaller.
func TestStdColorNamesAreAccepted(t *testing.T) {
	have := map[string]bool{}
	for _, c := range config.ColorNames {
		have[c] = true
	}
	for _, c := range ThemeStdColorNames {
		if !have[c] {
			t.Errorf("the standard's colour %q is not in config.ColorNames", c)
		}
	}
	accent := map[string]bool{}
	for _, c := range config.AccentNames {
		accent[c] = true
	}
	for _, c := range ThemeStdAccentNames {
		if !accent[c] {
			t.Errorf("the standard's accent %q is not offered by config.AccentNames", c)
		}
	}
	for _, f := range ThemeStdBannerFonts {
		if err := config.ValidateKey("BANNER_FONT", f); err != nil {
			t.Errorf("the standard's banner font %q is rejected here: %v", f, err)
		}
	}
	for _, e := range ThemeStdMenuEffects {
		if err := config.ValidateKey("MENU_EFFECT", e); err != nil {
			t.Errorf("the standard's menu effect %q is rejected here: %v", e, err)
		}
	}
}

// Every colour name the config accepts must exist in the palette, or choosing
// it silently falls back and the setting does nothing.
func TestEveryColorNameIsInThePalette(t *testing.T) {
	for _, name := range config.ColorNames {
		if name == "accent" || name == "default" {
			continue // resolved by colorFor, not looked up in the palette
		}
		if _, ok := namedColors[name]; !ok {
			t.Errorf("config offers the colour %q, but the palette has no such entry — it would paint the fallback", name)
		}
	}
	for _, name := range config.AccentNames {
		cfg := config.Default()
		if err := config.Apply(cfg, "THEME", name); err != nil {
			t.Fatal(err)
		}
		applyStyle(cfg)
		if AccentColor != namedColors[name] {
			t.Errorf("THEME=%s did not become the accent (got %v)", name, AccentColor)
		}
	}
	t.Cleanup(func() { applyStyle(config.Default()) })
}

// The old name for the accent key must keep working, or this rename logs
// people out of their own theme.
func TestLegacyAccentKeyStillReads(t *testing.T) {
	doc, err := config.Parse([]byte("COLOR_ACCENT=\"green\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := doc.Get("COLOR_ACCENT"); !ok || v != "green" {
		t.Fatalf("parse lost the legacy key: %q %v", v, ok)
	}
	// Written through Save, the old key is replaced by the new one.
	if strings.Contains(strings.Join(config.FieldKeys(), " "), "COLOR_ACCENT") {
		t.Error("COLOR_ACCENT is still in the field table; it is the old name")
	}
}
