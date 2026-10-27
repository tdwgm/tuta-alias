package creds

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/vault"
)

const (
	vtPass  = "vault passphrase 1"
	vtEmail = "alice@tuta.com"
	vtPW    = "login-password-xyz"
)

// vaultEnv points HOME and the cache dir at a temp dir (the vault path and the
// reboot cache both derive from them) and starts from an empty mock keyring,
// so no test sees another's cached passphrase.
func vaultEnv(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	_ = Clear(vaultCacheKey)
	t.Cleanup(func() { _ = Clear(vaultCacheKey) })
	return &config.Config{CredBackend: "vault", CacheTTL: "forever"}
}

func saveVault(t *testing.T, pass string) {
	t.Helper()
	if err := vault.Save(pass, vault.Entry{Email: vtEmail, Password: vtPW}); err != nil {
		t.Fatal(err)
	}
}

// countingPrompt answers every prompt with pass and counts the calls.
func countingPrompt(pass string, calls *int) Prompter {
	return func(string) (string, error) {
		*calls++
		return pass, nil
	}
}

func TestVaultBackend_NameAndCacheKey(t *testing.T) {
	if got := (vaultBackend{}).Name(); got != "vault" {
		t.Errorf("Name = %q", got)
	}
	if VaultCacheKey != "vault-passphrase" {
		t.Errorf("VaultCacheKey = %q", VaultCacheKey)
	}
}

// Available is called from doctor and the backend picker, so it must neither
// prompt nor create the file it is looking for.
func TestVaultBackend_Available(t *testing.T) {
	vaultEnv(t)
	err := vaultBackend{}.Available()
	if err == nil {
		t.Fatal("Available() = nil with no vault file")
	}
	if !strings.Contains(err.Error(), vault.Path()) {
		t.Errorf("error %q should name the file %s", err, vault.Path())
	}
	if _, serr := os.Stat(vault.Path()); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("Available created something: stat err = %v", serr)
	}

	saveVault(t, vtPass)
	if err := (vaultBackend{}).Available(); err != nil {
		t.Errorf("Available() with a vault = %v", err)
	}
}

func TestVaultBackend_FetchPromptsOnceThenUsesCache(t *testing.T) {
	cfg := vaultEnv(t)
	saveVault(t, vtPass)
	b := vaultBackend{}
	calls := 0
	var verbose bytes.Buffer

	c, err := b.Fetch(cfg, countingPrompt(vtPass, &calls), &verbose)
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != vtEmail || c.Password != vtPW {
		t.Errorf("Creds = %+v", c)
	}
	if calls != 1 {
		t.Errorf("first Fetch prompted %d times, want 1", calls)
	}
	if got, ok := Get(vaultCacheKey, cfg.CacheTTL); !ok || got != vtPass {
		t.Errorf("passphrase not cached: %q, %v", got, ok)
	}
	if strings.Contains(verbose.String(), vtPass) || strings.Contains(verbose.String(), vtPW) {
		t.Errorf("verbose output leaks a secret: %q", verbose.String())
	}

	// Second call: a prompter that would fail the test proves the cache served it.
	c, err = b.Fetch(cfg, func(string) (string, error) {
		t.Error("second Fetch prompted although the passphrase is cached")
		return "", errors.New("unexpected prompt")
	}, nil)
	if err != nil || c.Email != vtEmail || c.Password != vtPW {
		t.Errorf("second Fetch = %+v, %v", c, err)
	}
}

// The vault can be re-saved under another passphrase (from another shell, or
// on another device and copied over); the cache must heal, not wedge.
func TestVaultBackend_StaleCachedPassphrase(t *testing.T) {
	cfg := vaultEnv(t)
	saveVault(t, vtPass)
	b := vaultBackend{}
	calls := 0
	if _, err := b.Fetch(cfg, countingPrompt(vtPass, &calls), nil); err != nil {
		t.Fatal(err)
	}

	saveVault(t, "a different passphrase")
	calls = 0
	c, err := b.Fetch(cfg, countingPrompt("a different passphrase", &calls), nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("stale cache: prompted %d times, want 1", calls)
	}
	if c.Email != vtEmail || c.Password != vtPW {
		t.Errorf("Creds = %+v", c)
	}
	if got, _ := Get(vaultCacheKey, cfg.CacheTTL); got != "a different passphrase" {
		t.Errorf("cache holds %q, want the new passphrase", got)
	}
}

// When the stale passphrase is replaced by another wrong one, the cache must
// end up empty rather than keep the dead passphrase.
func TestVaultBackend_StaleCacheClearedEvenIfRetypeFails(t *testing.T) {
	cfg := vaultEnv(t)
	saveVault(t, "the real passphrase")
	if err := Cache(vaultCacheKey, "stale", cfg.CacheTTL); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err := vaultBackend{}.Fetch(cfg, countingPrompt("also wrong", &calls), nil)
	if !errors.Is(err, vault.ErrWrongPassphrase) {
		t.Errorf("err = %v, want it to wrap vault.ErrWrongPassphrase", err)
	}
	if calls != 1 {
		t.Errorf("prompted %d times, want 1", calls)
	}
	if got, ok := Get(vaultCacheKey, cfg.CacheTTL); ok {
		t.Errorf("dead passphrase still cached: %q", got)
	}
}

func TestVaultBackend_WrongTypedPassphraseIsNotCached(t *testing.T) {
	cfg := vaultEnv(t)
	saveVault(t, vtPass)
	calls := 0
	_, err := vaultBackend{}.Fetch(cfg, countingPrompt("nope", &calls), nil)
	if !errors.Is(err, vault.ErrWrongPassphrase) {
		t.Errorf("err = %v, want vault.ErrWrongPassphrase", err)
	}
	if got, ok := Get(vaultCacheKey, cfg.CacheTTL); ok {
		t.Errorf("a passphrase that did not open the vault was cached: %q", got)
	}
}

func TestVaultBackend_NilPrompt(t *testing.T) {
	cfg := vaultEnv(t)
	saveVault(t, vtPass)
	if _, err := (vaultBackend{}).Fetch(cfg, nil, nil); err == nil {
		t.Error("Fetch with nil prompt and no cache succeeded")
	}
}

func TestVaultBackend_PromptErrorPropagates(t *testing.T) {
	cfg := vaultEnv(t)
	saveVault(t, vtPass)
	want := errors.New("user cancelled")
	_, err := vaultBackend{}.Fetch(cfg, func(string) (string, error) { return "", want }, nil)
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want the prompt's error", err)
	}
}

func TestVaultBackend_FetchWithoutVault(t *testing.T) {
	cfg := vaultEnv(t)
	_, err := vaultBackend{}.Fetch(cfg, func(string) (string, error) {
		t.Error("prompted for a passphrase although there is no vault")
		return "", errors.New("unexpected prompt")
	}, nil)
	if err == nil {
		t.Error("Fetch without a vault succeeded")
	}
}

// A corrupt vault is not fixed by another passphrase; the cached one may be
// fine for the repaired file, so it must survive.
func TestVaultBackend_CorruptVaultKeepsCache(t *testing.T) {
	cfg := vaultEnv(t)
	saveVault(t, vtPass)
	if err := Cache(vaultCacheKey, vtPass, cfg.CacheTTL); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vault.Path(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := vaultBackend{}.Fetch(cfg, func(string) (string, error) {
		t.Error("prompted for a corrupt vault")
		return "", errors.New("unexpected prompt")
	}, nil)
	if !errors.Is(err, vault.ErrCorrupt) {
		t.Errorf("err = %v, want vault.ErrCorrupt", err)
	}
	if _, ok := Get(vaultCacheKey, cfg.CacheTTL); !ok {
		t.Error("cached passphrase was dropped over a corrupt file")
	}
}

// The vault never holds a TOTP secret, so whatever the backend returns must
// leave 2FA to be asked for separately.
func TestVaultBackend_TOTPAlwaysEmpty(t *testing.T) {
	cfg := vaultEnv(t)
	saveVault(t, vtPass)
	calls := 0
	for _, name := range []string{"prompted", "cached"} {
		c, err := vaultBackend{}.Fetch(cfg, countingPrompt(vtPass, &calls), nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c.TOTP != "" {
			t.Errorf("%s: TOTP = %q, want empty", name, c.TOTP)
		}
	}
}

func TestVaultBackend_ClearCache(t *testing.T) {
	cfg := vaultEnv(t)
	saveVault(t, vtPass)
	b := vaultBackend{}
	calls := 0
	if _, err := b.Fetch(cfg, countingPrompt(vtPass, &calls), nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := Get(vaultCacheKey, cfg.CacheTTL); !ok {
		t.Fatal("setup: passphrase not cached")
	}
	if err := b.ClearCache(cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := Get(vaultCacheKey, cfg.CacheTTL); ok {
		t.Error("passphrase still cached after ClearCache")
	}
	if err := b.ClearCache(cfg); err != nil {
		t.Errorf("ClearCache on an empty cache = %v, want nil", err)
	}
	// ClearCache forgets the passphrase, not the vault.
	if !vault.Exists() {
		t.Error("ClearCache removed the vault file")
	}
}
