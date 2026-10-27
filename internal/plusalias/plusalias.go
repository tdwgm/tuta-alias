// Package plusalias manages locally-stored "+" (sub-)addresses.
//
// Plus addressing (a.k.a. sub-addressing) routes mail for spam+anything@d
// straight to spam@d on the server, so generating one needs NO API call —
// it is purely local. This package generates suffixes, composes addresses,
// and persists them (with an optional note) to a JSON file alongside the
// shell-style config. ONLY "+" addresses live here; every other address is
// managed through the Tuta API by the services package.
package plusalias

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tdwgm/tuta-alias/internal/atomicfile"
)

// Entry is one saved plus-address with its note and creation date.
type Entry struct {
	Address string `json:"address"`
	Note    string `json:"note"`
	Created string `json:"created"` // YYYY-MM-DD
}

// suffixAlphabet is the charset for auto-generated suffixes: lowercase
// letters and digits (matches the user's "spam+jv5d8z2x" example style).
const suffixAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// validSuffixRE allows what mail sub-addressing realistically accepts in
// the part after "+": alphanumerics plus dot, hyphen, underscore.
var validSuffixRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// Path returns the JSON store path: ~/.config/tuta-alias/plus-aliases.json.
// It mirrors config.Path()'s hardcoded location (no XDG override) so the
// store always sits next to tuta-alias.conf.
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "tuta-alias", "plus-aliases.json")
}

// Generate returns n random characters from the suffix alphabet. n is
// floored to at least 1 so a misconfigured length never yields "".
func Generate(n int) (string, error) {
	if n < 1 {
		n = 1
	}
	var b strings.Builder
	b.Grow(n)
	max := big.NewInt(int64(len(suffixAlphabet)))
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(suffixAlphabet[idx.Int64()])
	}
	return b.String(), nil
}

// SplitBase splits an address into its base local-part (stripping any
// existing "+suffix") and domain. "spam+old@example.com" → ("spam","example.com").
// If there is no "@", domain is empty and local is the whole input.
func SplitBase(addr string) (local, domain string) {
	addr = strings.TrimSpace(addr)
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		local, domain = addr[:i], addr[i+1:]
	} else {
		local = addr
	}
	if p := strings.IndexByte(local, '+'); p >= 0 {
		local = local[:p]
	}
	return local, domain
}

// Compose builds local+suffix@domain.
func Compose(local, suffix, domain string) string {
	return fmt.Sprintf("%s+%s@%s", local, suffix, domain)
}

// ValidSuffix reports whether s is a non-empty, structurally-valid suffix.
func ValidSuffix(s string) bool {
	return s != "" && validSuffixRE.MatchString(s)
}

// Load reads the store. A missing file is not an error — it returns an
// empty slice so callers can append unconditionally.
func Load() ([]Entry, error) {
	p := Path()
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return []Entry{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return []Entry{}, nil
	}
	var out []Entry
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return out, nil
}

// Save writes entries to the store atomically, creating the config dir if
// needed.
func Save(entries []Entry) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(p), err)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicfile.Write(p, data, 0o600)
}

// Append loads the store, appends e, and saves it back.
func Append(e Entry) error {
	entries, err := Load()
	if err != nil {
		return err
	}
	entries = append(entries, e)
	return Save(entries)
}
