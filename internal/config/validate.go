// validate.go — one validator for every config key, used by the Config
// screen, `tuta-alias config set` and `tuta-alias config check`.
//
// Before this file only TUTA_PROXY was checked. Everything else was accepted
// as typed and then silently reinterpreted: PLUS_SUFFIX_LEN="abc" became 5,
// SERVE_PORT="x" became 0, and an invalid SHOW_ALIASES_FOR regexp degraded to
// substring matching without a word. A setting that does not do what it says
// is worse than one that refuses to be set.
package config

import (
	"fmt"
	"math"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tdwgm/tuta-alias/internal/api"
)

// BackendNames are the accepted CRED_BACKEND values ("" = type it every time).
var BackendNames = []string{"", "bitwarden", "bwrun", "keepassxc", "keyring", "vault"}

// CacheTTLChoices are the TTL values the UIs offer. Any Go duration works.
var CacheTTLChoices = []string{"8h", "1h", "30m", "reboot", "forever"}

// CacheTTLMode classifies a CACHE_TTL string: "reboot" (tmpfs, cleared by a
// reboot), "forever" (keyring entry without expiry) or "duration".
// internal/creds delegates to this so the vocabulary has one definition.
func CacheTTLMode(ttl string) string {
	switch strings.ToLower(strings.TrimSpace(ttl)) {
	case "reboot":
		return "reboot"
	case "forever", "permanent", "always":
		return "forever"
	default:
		return "duration"
	}
}

// ParseCacheTTL parses a "duration"-mode CACHE_TTL. Go duration strings
// ("8h", "1h30m") and a bare integer as hours ("8" = 8h) are accepted.
func ParseCacheTTL(ttl string) (time.Duration, error) {
	ttl = strings.TrimSpace(ttl)
	if n, err := strconv.Atoi(ttl); err == nil {
		// A bare number is hours, and hours overflow: 2562048 of them is more
		// than a time.Duration holds, so "3000000" wrapped to a NEGATIVE
		// duration that ValidateKey accepted. The cache then stored entries
		// that were already expired and Get never hit one again, so a
		// credential backend asked for the master password every single time
		// with nothing to show why.
		if n < 0 {
			return 0, fmt.Errorf("CACHE_TTL must not be negative, not %q", ttl)
		}
		if n > maxCacheTTLHours {
			return 0, fmt.Errorf("CACHE_TTL in hours must be at most %d, not %q (use \"forever\")",
				maxCacheTTLHours, ttl)
		}
		return time.Duration(n) * time.Hour, nil
	}
	d, err := time.ParseDuration(ttl)
	if err != nil {
		return 0, err
	}
	// time.ParseDuration refuses what does not fit, so the only negative here
	// is one the user wrote ("-8h"), which the same reasoning rejects.
	if d < 0 {
		return 0, fmt.Errorf("CACHE_TTL must not be negative, not %q", ttl)
	}
	return d, nil
}

// maxCacheTTLHours is how many whole hours a time.Duration can hold. Anything
// above it is a mistake, and "forever" is the option for meaning it.
const maxCacheTTLHours = int(math.MaxInt64 / int64(time.Hour))

// Choices returns the fixed choice list for a key, or nil when the key is
// free text. The Config screen renders a radio list for every non-nil result,
// so a key gains its choices here and nowhere else.
func Choices(key string) []string {
	switch key {
	case "TUI_ENABLED", "SHOW_QUOTA", "COMPLETION_BASH", "COMPLETION_ZSH", "COMPLETION_FISH":
		return []string{"true", "false"}
	case "CRED_BACKEND":
		return BackendNames
	case "CACHE_TTL":
		return CacheTTLChoices
	case "LOG_LEVEL":
		return LogLevels
	case "AUTO_UPDATE":
		return AutoUpdateModes
	case "THEME":
		return AccentNames
	case "BANNER_FONT":
		return BannerFonts
	case "LIVE_INFO":
		return LiveInfoModes
	case "MENU_EFFECT":
		return MenuEffects
	}
	if strings.HasPrefix(key, "COLOR_") {
		return ColorNames
	}
	return nil
}

// isControl reports characters the one-line KEY="value" format cannot carry.
func isControl(r rune) bool {
	return r == '\n' || r == '\r' || r == 0
}

// FieldKeys returns every config key, in file order.
func FieldKeys() []string {
	out := make([]string, 0, len(Fields))
	for _, f := range Fields {
		out = append(out, f.Key)
	}
	return out
}

// Known reports whether key is a config key.
func Known(key string) bool {
	for _, f := range Fields {
		if f.Key == key {
			return true
		}
	}
	return false
}

// Lookup returns the field table row for key.
func Lookup(key string) (Field, bool) {
	for _, f := range Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// ValidateKey reports why value is not usable for key, or nil when it is.
// An empty value is accepted for every key whose default is empty: that is
// how a setting is turned off.
func ValidateKey(key, value string) error {
	if !Known(key) {
		return fmt.Errorf("unknown config key %q", key)
	}
	// A control character would not survive the file format: the writer puts
	// KEY="value" on one line, so a newline inside the value splits it, and
	// the second half is read back as another key. `config set BANNER_TEXT
	// $'x\nINSTALL_PATH="/elsewhere"'` injected a key that way.
	if i := strings.IndexFunc(value, isControl); i >= 0 {
		return fmt.Errorf("%s must not contain control characters (found %q at position %d)", key, value[i], i)
	}
	v := strings.TrimSpace(value)

	if list := Choices(key); list != nil {
		switch {
		case key == "BANNER_FONT" && strings.EqualFold(v, "default"):
			return nil // the pre-0.12 name for "logo"
		case key == "CACHE_TTL":
			// The choice list is the five values the UIs offer, not a closed
			// set: the key is documented as "a Go duration", and creds reads
			// any of them. Refusing "2h" here while the loader accepts it is
			// the kind of mismatch this file exists to prevent.
			if CacheTTLMode(v) != "duration" {
				return nil
			}
			if _, err := ParseCacheTTL(v); err != nil {
				return fmt.Errorf("CACHE_TTL must be \"reboot\", \"forever\" or a duration like \"8h\", not %q", value)
			}
			return nil
		case len(list) == 2 && list[0] == "true" && list[1] == "false":
			// Accept the loader's whole vocabulary for a bool, or `config
			// set TUI_ENABLED yes` is refused for a value the config file
			// itself reads as true.
			switch strings.ToLower(v) {
			case "true", "false", "1", "0", "yes", "no":
				return nil
			}
			return fmt.Errorf("%s must be true or false, not %q", key, value)
		}
		for _, c := range list {
			if strings.EqualFold(v, c) {
				return nil
			}
		}
		shown := make([]string, 0, len(list))
		for _, c := range list {
			if c == "" {
				c = `""`
			}
			shown = append(shown, c)
		}
		return fmt.Errorf("%s must be one of: %s", key, strings.Join(shown, " | "))
	}

	switch key {
	case "PLUS_SUFFIX_LEN":
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 64 {
			return fmt.Errorf("PLUS_SUFFIX_LEN must be a whole number 1-64, not %q", value)
		}
	case "SERVE_PORT":
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("SERVE_PORT must be a port number 1-65535, not %q", value)
		}
	case "SERVE_HOST":
		if v == "" {
			return fmt.Errorf("SERVE_HOST must be an address, e.g. 127.0.0.1")
		}
		if ip := net.ParseIP(v); ip == nil && v != "localhost" {
			return fmt.Errorf("SERVE_HOST must be an IP address or \"localhost\", not %q", value)
		}
	case "SHOW_ALIASES_FOR":
		if v == "" {
			return nil
		}
		if _, err := regexp.Compile(v); err != nil {
			return fmt.Errorf("SHOW_ALIASES_FOR is not a valid regexp: %w", err)
		}
	case "UPDATE_INTERVAL":
		if _, err := time.ParseDuration(v); err != nil {
			return fmt.Errorf("UPDATE_INTERVAL must be a duration like \"24h\", not %q", value)
		}
	case "UPDATE_REF":
		// A ref goes to `git ls-remote` as an argument, never through a
		// shell, but a value with whitespace or a leading dash would still be
		// read as a flag or as two arguments.
		if v == "" {
			return fmt.Errorf("UPDATE_REF must be a branch or tag name")
		}
		if strings.HasPrefix(v, "-") || strings.ContainsAny(v, " \t~^:?*[\\") {
			return fmt.Errorf("UPDATE_REF must be a plain branch or tag name, not %q", value)
		}
	case "CACHE_TTL":
		if CacheTTLMode(v) == "duration" {
			if _, err := ParseCacheTTL(v); err != nil {
				return fmt.Errorf("CACHE_TTL must be \"reboot\", \"forever\" or a duration like \"8h\", not %q", value)
			}
		}
	case "TUTA_PROXY":
		if _, err := api.ParseProxy(v); err != nil {
			return err
		}
	case "INSTALL_PATH", "LOG_FILE":
		if v != "" && !filepath.IsAbs(v) {
			return fmt.Errorf("%s must be an absolute path, not %q", key, value)
		}
	case "DEFAULT_DOMAIN":
		if v != "" && (strings.ContainsAny(v, " @/") || !strings.Contains(v, ".")) {
			return fmt.Errorf("DEFAULT_DOMAIN must be a bare domain like example.com, not %q", value)
		}
	}
	return nil
}

// Canonical returns value as the choice list spells it, for a key that has
// one. ValidateKey accepts a choice in any case, because a hand-edited file
// should not fail over a capital letter, but every consumer compares the
// value exactly: `config set CRED_BACKEND BitWarden` was accepted and then
// matched nothing, so the backend was silently inactive. Normalising on the
// way in is what makes the accepting and the comparing agree.
func Canonical(key, value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return value
	}
	for _, c := range Choices(key) {
		if c != "" && strings.EqualFold(v, c) {
			return c
		}
	}
	return value
}

// Validate checks every key in a parsed config file. It returns one error per
// bad value, the keys the file holds that this version does not know, and the
// keys that are a former name of a current one.
//
// The legacy names are reported apart from the unknown ones because they are
// not unknown: the loader reads COLOR_ACCENT as THEME and Save rewrites it, so
// calling it "a key this version does not know" sent the user looking for a
// problem that does not exist.
func Validate(doc *Doc) (problems []error, unknown, legacy []string) {
	for _, e := range doc.entries {
		if e.kind != kindKV {
			continue
		}
		if now, ok := legacyKeys[e.key]; ok {
			legacy = append(legacy, e.key+" (now "+now+")")
			if err := ValidateKey(now, e.value); err != nil {
				problems = append(problems, err)
			}
			continue
		}
		if !Known(e.key) {
			unknown = append(unknown, e.key)
			continue
		}
		if err := ValidateKey(e.key, e.value); err != nil {
			problems = append(problems, err)
		}
	}
	return problems, unknown, legacy
}
