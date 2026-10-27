package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/tdwgm/tuta-alias/internal/config"
)

// The README is part of the 1:1 contract, not commentary on it. A key or a
// command that exists in the program and not in the README is the same defect
// as one that exists in the file and not on the Config screen: the user is
// told something that is not true, or not told something that is.
//
// This is deliberately a presence check, not a prose check. It cannot tell
// whether the README describes a key correctly, only that the key is not
// missing from it entirely, which is the failure that actually happened.

func readme(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("cannot locate this file")
	}
	// cmd/tuta-alias -> cmd -> <project>
	p := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(self))), "README.md")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(raw)
}

func TestReadmeDocumentsEveryConfigKey(t *testing.T) {
	doc := readme(t)
	for _, f := range config.Fields {
		if !strings.Contains(doc, f.Key) {
			t.Errorf("the README never mentions the config key %s", f.Key)
		}
	}
}

func TestReadmeDocumentsEveryCommand(t *testing.T) {
	doc := readme(t)
	for _, c := range buildRoot().Commands() {
		if c.Hidden || !c.IsAvailableCommand() {
			continue
		}
		// cobra generates `completion` itself; the README mentions it with
		// the shells rather than on its own line.
		if !strings.Contains(doc, "tuta-alias "+c.Name()) {
			t.Errorf("the README never shows `tuta-alias %s`", c.Name())
		}
	}
}

// The README must not promise a key the program does not have.
//
// The check is derived, not a list someone remembers to update: every
// SCREAMING_CASE token in the README that looks like a config key must be
// one, or be explained as the old name of one. A hardcoded list of stale
// names went out of date the moment three of them became real keys.
func TestReadmeHasNoStaleConfigKeys(t *testing.T) {
	doc := readme(t)
	// Not config keys: environment variables and headers the README names.
	notAKey := map[string]bool{
		"TUTA_PASSWORD": true, "TUTA_TOTP": true, "TUTA_EMAIL": true,
		"XDG_CACHE_HOME": true, "XDG_CONFIG_HOME": true, "XDG_RUNTIME_DIR": true,
		"EDITOR": true, "VISUAL": true, "PATH": true, "HOME": true,
		"CGO_ENABLED": true, "GOOS": true, "GOARCH": true, "BW_SESSION": true,
		// The hooks' switches, and the CI step's own variables.
		"VERSION_TAG_ANY_BRANCH": true, "SKIP_VERSION_TAG": true,
		"SKIP_VERSION_BUMP": true, "BREAKING_CHANGE": true,
	}
	keyLike := regexp.MustCompile(`\b[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+\b`)
	for _, line := range strings.Split(doc, "\n") {
		for _, token := range keyLike.FindAllString(line, -1) {
			if config.Known(token) || notAKey[token] {
				continue
			}
			lower := strings.ToLower(line)
			if strings.Contains(lower, "old name") || strings.Contains(lower, "renamed") ||
				strings.Contains(lower, "still read") || strings.Contains(lower, "before 0.12") {
				continue // a documented legacy name
			}
			t.Errorf("the README names %s, which is not a config key and is not explained as an old one:\n  %s",
				token, strings.TrimSpace(line))
		}
	}
}
