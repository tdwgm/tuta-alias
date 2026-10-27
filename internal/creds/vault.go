package creds

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/vault"
)

// vaultCacheKey is the keyring user under which the vault passphrase is cached
// (per CACHE_TTL, like the KeePassXC master). The vault holds email + password
// only; the TOTP code is asked for on every login so 2FA stays a separate
// factor.
const vaultCacheKey = "vault-passphrase"

// VaultCacheKey is the cache key Options uses for status/clear.
const VaultCacheKey = vaultCacheKey

// vaultCreateHint tells the user how to make the vault file Available looks for.
const vaultCreateHint = "tuta-alias vault set"

// vaultBackend serves the login stored in the encrypted vault file
// (internal/vault) — the option for platforms without a Secret Service.
type vaultBackend struct{}

func (vaultBackend) Name() string { return "vault" }

// Available reports whether the vault file exists. It never prompts and never
// creates anything.
func (vaultBackend) Available() error {
	if !vault.Exists() {
		return fmt.Errorf("no encrypted vault at %s — create it with %q", vault.Path(), vaultCreateHint)
	}
	return nil
}

// Fetch opens the vault with a passphrase from the cache or the prompt. A
// cached passphrase that no longer opens the vault is dropped and the user is
// asked again. TOTP is always empty.
func (b vaultBackend) Fetch(cfg *config.Config, prompt Prompter, verbose io.Writer) (Creds, error) {
	if err := b.Available(); err != nil {
		return Creds{}, err
	}

	if pass, ok := Get(vaultCacheKey, cfg.CacheTTL); ok {
		vlog(verbose, "using cached vault passphrase")
		e, err := vault.Open(pass)
		if err == nil {
			return Creds{Email: e.Email, Password: e.Password}, nil
		}
		if !errors.Is(err, vault.ErrWrongPassphrase) {
			// Missing or corrupt file: another passphrase will not help, and
			// the cached one may be perfectly good for a repaired vault.
			return Creds{}, fmt.Errorf("open vault: %w", err)
		}
		_ = Clear(vaultCacheKey)
		vlog(verbose, "cached vault passphrase rejected — re-prompting")
	}

	if prompt == nil {
		return Creds{}, errors.New("no vault-passphrase prompt given — internal bug")
	}
	vlog(verbose, "vault: open "+vault.Path())
	pass, err := prompt("Vault passphrase: ")
	if err != nil {
		return Creds{}, err
	}
	e, err := vault.Open(pass)
	if err != nil {
		return Creds{}, fmt.Errorf("open vault: %w", err)
	}
	if err := Cache(vaultCacheKey, pass, cfg.CacheTTL); err != nil {
		fmt.Fprintln(os.Stderr, "[vault] warning: could not cache passphrase:", err)
	}
	return Creds{Email: e.Email, Password: e.Password}, nil
}

func (vaultBackend) ClearCache(_ *config.Config) error { return Clear(vaultCacheKey) }
