package creds

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	keyringlib "github.com/zalando/go-keyring"

	"github.com/tdwgm/tuta-alias/internal/atomicfile"
	"github.com/tdwgm/tuta-alias/internal/config"
)

const keyringService = "tuta-alias"

// legacyKeyringService is the service name used until 0.10.0, when the project
// was still called tuta-cli. keyringGet falls back to it once and moves the
// entry over, so a saved login survives the rename.
const legacyKeyringService = "tuta-cli"

// keyringGet reads user's entry from the keyring. On a miss it looks under the
// pre-0.10.0 service name and, when it finds something there, copies it to the
// current service and removes the old entry. A failed copy is not fatal: the
// value is still returned, and the next call retries the move.
func keyringGet(user string) (string, error) {
	raw, err := keyringlib.Get(keyringService, user)
	if !errors.Is(err, keyringlib.ErrNotFound) {
		return raw, err
	}
	legacyRaw, lerr := keyringlib.Get(legacyKeyringService, user)
	if lerr != nil {
		// Report the original miss, not the legacy lookup's error.
		return "", err
	}
	if serr := keyringlib.Set(keyringService, user, legacyRaw); serr == nil {
		_ = keyringlib.Delete(legacyKeyringService, user)
	}
	return legacyRaw, nil
}

// CacheStatus describes whether a cached secret exists and how it's bounded.
type CacheStatus struct {
	Exists    bool
	Mode      string    // "duration" | "reboot" | "forever" | "empty"
	ExpiresAt time.Time // zero for forever; ignored if Mode == "reboot"
}

// TTLMode classifies a CACHE_TTL string into one of three modes:
//   - "reboot"   — store in /run/user/$UID/...
//   - "forever"  — keyring entry without expiry
//   - "duration" — keyring entry with a JSON-embedded expiry
//
// The vocabulary is defined once, in internal/config, so `config check` and
// the Config screen judge a CACHE_TTL value exactly as this package will.
func TTLMode(ttl string) string { return config.CacheTTLMode(ttl) }

// parseDuration accepts Go duration strings ("8h", "30m", "1h30m") and a bare
// integer as hours ("8" → 8h). Returns (0, error) on parse failure.
func parseDuration(ttl string) (time.Duration, error) { return config.ParseCacheTTL(ttl) }

// cacheEntry is what the keyring holds under a cache key. Only this package
// writes and reads it, but an entry outlives the build that wrote it.
type cacheEntry struct {
	Secret string `json:"secret"`
	// ExpiresAt is zero for an entry that never expires, and omitzero leaves it
	// out of the JSON then. omitempty cannot: encoding/json never treats a
	// struct as empty, so every such entry carried "0001-01-01T00:00:00Z".
	// Both shapes read back as no expiry, the field being absent or being the
	// zero time, so the entries already in a keyring stay valid and a build
	// from before this can read the new ones.
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
}

// Cache stores secret keyed by `user`. The TTL string follows the same
// vocabulary as the CACHE_TTL config key.
func Cache(user, secret, ttl string) error {
	switch TTLMode(ttl) {
	case "reboot":
		return writeRebootCache(user, secret)
	case "forever":
		return setKeyring(user, cacheEntry{Secret: secret})
	default:
		dur, err := parseDuration(ttl)
		if err != nil {
			return fmt.Errorf("invalid CACHE_TTL %q: %w", ttl, err)
		}
		return setKeyring(user, cacheEntry{
			Secret:    secret,
			ExpiresAt: time.Now().Add(dur),
		})
	}
}

// Get returns the cached secret if it exists and hasn't expired.
// Looks in both the keyring and the reboot tmpfs path.
func Get(user, ttl string) (string, bool) {
	switch TTLMode(ttl) {
	case "reboot":
		return readRebootCache(user)
	default:
		secret, err := getKeyringSecret(user)
		return secret, err == nil
	}
}

// getKeyringSecret returns the unexpired secret stored for user. It returns
// keyringlib.ErrNotFound when nothing is stored (or the entry has expired and
// was deleted), and a wrapped error when the keyring is unreachable or locked
// or the entry is not valid JSON.
func getKeyringSecret(user string) (string, error) {
	raw, err := keyringGet(user)
	if errors.Is(err, keyringlib.ErrNotFound) {
		return "", keyringlib.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read keyring entry %q: %w", user, err)
	}
	var e cacheEntry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return "", fmt.Errorf("decode keyring entry %q: %w", user, err)
	}
	if !e.ExpiresAt.IsZero() && time.Now().After(e.ExpiresAt) {
		_ = keyringlib.Delete(keyringService, user)
		return "", keyringlib.ErrNotFound
	}
	return e.Secret, nil
}

// Clear removes any cached secret for user (both keyring and tmpfs).
func Clear(user string) error {
	var errs []error
	if err := keyringlib.Delete(keyringService, user); err != nil && !errors.Is(err, keyringlib.ErrNotFound) {
		errs = append(errs, err)
	}
	// Also drop anything still stored under the pre-0.10.0 service name, so a
	// purge does not leave a secret behind that keyringGet would resurrect.
	if err := keyringlib.Delete(legacyKeyringService, user); err != nil && !errors.Is(err, keyringlib.ErrNotFound) {
		errs = append(errs, err)
	}
	if p := rebootCachePath(user); p != "" {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// PurgeCaches removes every secret tuta-alias keeps in the keyring or the
// reboot cache: the Bitwarden session, the saved login, the vault passphrase
// and — when keePassDB is set — the KeePassXC master for that database. Used
// by uninstall --purge. (The vault FILE is user data and goes with the config
// directory, not from here.)
func PurgeCaches(keePassDB string) error {
	keys := []string{bwSessionKey, savedLoginKey, vaultCacheKey}
	if keePassDB != "" {
		keys = append(keys, KeePassCacheKey(keePassDB))
	}
	var errs []error
	for _, k := range keys {
		if err := Clear(k); err != nil {
			errs = append(errs, fmt.Errorf("clear %s: %w", k, err))
		}
	}
	return errors.Join(errs...)
}

// Status reports the current cache state for the user key under the given
// TTL policy. Used by Options to render "valid, 4h 12m left" etc.
func Status(user, ttl string) CacheStatus {
	switch TTLMode(ttl) {
	case "reboot":
		if _, ok := readRebootCache(user); ok {
			return CacheStatus{Exists: true, Mode: "reboot"}
		}
		return CacheStatus{Mode: "empty"}
	case "forever":
		raw, err := keyringGet(user)
		if err != nil {
			return CacheStatus{Mode: "empty"}
		}
		var e cacheEntry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return CacheStatus{Mode: "empty"}
		}
		return CacheStatus{Exists: true, Mode: "forever"}
	default:
		raw, err := keyringGet(user)
		if err != nil {
			return CacheStatus{Mode: "empty"}
		}
		var e cacheEntry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return CacheStatus{Mode: "empty"}
		}
		if !e.ExpiresAt.IsZero() && time.Now().After(e.ExpiresAt) {
			return CacheStatus{Mode: "empty"}
		}
		return CacheStatus{Exists: true, Mode: "duration", ExpiresAt: e.ExpiresAt}
	}
}

func setKeyring(user string, e cacheEntry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return keyringlib.Set(keyringService, user, string(data))
}

// rebootCachePath returns the per-user runtime path for tmpfs-cached
// secrets: $XDG_RUNTIME_DIR (normally /run/user/$UID) or /run/user/$UID.
// Empty string on failure to detect uid.
func rebootCachePath(user string) string {
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		return filepath.Join(x, "tuta-alias", user)
	}
	uid := os.Getuid()
	if uid < 0 {
		return ""
	}
	return filepath.Join("/run", "user", fmt.Sprintf("%d", uid), "tuta-alias", user)
}

func writeRebootCache(user, secret string) error {
	p := rebootCachePath(user)
	if p == "" {
		return errors.New("cannot determine /run/user path")
	}
	// 0600 in a 0700 directory: this is a master password on tmpfs.
	return atomicfile.WriteMkdir(p, []byte(secret), 0o600, 0o700)
}

func readRebootCache(user string) (string, bool) {
	p := rebootCachePath(user)
	if p == "" {
		return "", false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return string(raw), true
}
