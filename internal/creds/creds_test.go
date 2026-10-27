package creds

import (
	"testing"
	"time"

	"github.com/tdwgm/tuta-alias/internal/config"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		backend string
		wantNil bool
		wantErr bool
	}{
		{"", true, false},
		{"bitwarden", false, false},
		{"keepassxc", false, false},
		{"bwrun", false, false},
		{"keyring", false, false},
		{"nonsense", false, true},
	}
	for _, c := range cases {
		cfg := &config.Config{CredBackend: c.backend}
		b, err := Resolve(cfg)
		if c.wantErr {
			if err == nil {
				t.Errorf("backend=%q: expected error", c.backend)
			}
			continue
		}
		if err != nil {
			t.Errorf("backend=%q: unexpected err: %v", c.backend, err)
		}
		if (b == nil) != c.wantNil {
			t.Errorf("backend=%q: nil=%v want %v", c.backend, b == nil, c.wantNil)
		}
	}
}

func TestTTLMode(t *testing.T) {
	cases := map[string]string{
		"8h":        "duration",
		"30m":       "duration",
		"":          "duration", // default falls through to duration parse
		"reboot":    "reboot",
		"Reboot":    "reboot",
		"REBOOT":    "reboot",
		"forever":   "forever",
		"FOREVER":   "forever",
		"always":    "forever",
		"permanent": "forever",
	}
	for in, want := range cases {
		if got := TTLMode(in); got != want {
			t.Errorf("TTLMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDuration(t *testing.T) {
	d, err := parseDuration("8h")
	if err != nil || d != 8*time.Hour {
		t.Errorf("parseDuration(8h) = %v, %v", d, err)
	}
	d, err = parseDuration("8")
	if err != nil || d != 8*time.Hour {
		t.Errorf("parseDuration(8) = %v, %v", d, err)
	}
	if _, err := parseDuration("nonsense"); err == nil {
		t.Errorf("parseDuration(nonsense) should fail")
	}
}

func TestBackendNames(t *testing.T) {
	if (bitwardenBackend{}).Name() != "bitwarden" {
		t.Errorf("bitwarden name")
	}
	if (keepassBackend{}).Name() != "keepassxc" {
		t.Errorf("keepass name")
	}
	if (bwrunBackend{}).Name() != "bwrun" {
		t.Errorf("bwrun name")
	}
	if (keyringBackend{}).Name() != "keyring" {
		t.Errorf("keyring name")
	}
}
