package cliflow

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/creds"
	"github.com/tdwgm/tuta-alias/internal/session"
	"github.com/tdwgm/tuta-alias/internal/vault"
)

// The vault commands. The vault is the credential backend for systems with no
// Secret Service keyring (Termux/Android above all), where the alternative is
// typing the password on every login.
//
// Nothing here takes a secret on the command line: a passphrase in argv is
// visible in `ps` and in the shell history. They are read from the terminal
// without echo, or from stdin when there is no terminal.

// VaultSet creates or replaces the vault.
func VaultSet(stdout, stderr io.Writer, in *os.File, email string) int {
	p := newLinePrompter(stdout, in)

	if email == "" {
		// Offer the logged-in address, since that is almost always the one.
		suggest := ""
		if s, err := session.Load(); err == nil {
			suggest = s.MailAddress
		}
		prompt := "Email: "
		if suggest != "" {
			prompt = fmt.Sprintf("Email [%s]: ", suggest)
		}
		v, err := p.line(prompt)
		if err != nil {
			return loginFailed(stderr, err)
		}
		if email = strings.TrimSpace(v); email == "" {
			email = suggest
		}
	}
	if email == "" {
		fmt.Fprintln(stderr, "an email address is required")
		return 2
	}

	password, err := p.password("Tuta password: ")
	if err != nil {
		return loginFailed(stderr, err)
	}
	if password == "" {
		fmt.Fprintln(stderr, "the password must not be empty")
		return 2
	}
	pass, err := p.password("Vault passphrase: ")
	if err != nil {
		return loginFailed(stderr, err)
	}
	again, err := p.password("Vault passphrase (again): ")
	if err != nil {
		return loginFailed(stderr, err)
	}
	if pass != again {
		fmt.Fprintln(stderr, "the passphrases do not match — nothing was written")
		return 2
	}
	if err := vault.Save(pass, vault.Entry{Email: email, Password: password}); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	// A passphrase cached for the old vault would be wrong for the new one.
	_ = creds.Clear(creds.VaultCacheKey)

	fmt.Fprintf(stdout, "wrote %s\n", vault.Path())
	if cfg, err := config.Load(); err == nil && cfg.CredBackend != "vault" {
		fmt.Fprintln(stdout, "To use it: tuta-alias config set CRED_BACKEND vault")
	}
	fmt.Fprintln(stdout, "The TOTP code is never stored; it is asked for at every login.")
	return 0
}

// VaultPasswd re-encrypts the vault under a new passphrase.
func VaultPasswd(stdout, stderr io.Writer, in *os.File) int {
	if !vault.Exists() {
		fmt.Fprintf(stderr, "no vault at %s — create it with `tuta-alias vault set`\n", vault.Path())
		return 2
	}
	p := newLinePrompter(stdout, in)
	old, err := p.password("Current vault passphrase: ")
	if err != nil {
		return loginFailed(stderr, err)
	}
	next, err := p.password("New vault passphrase: ")
	if err != nil {
		return loginFailed(stderr, err)
	}
	again, err := p.password("New vault passphrase (again): ")
	if err != nil {
		return loginFailed(stderr, err)
	}
	if next != again {
		fmt.Fprintln(stderr, "the passphrases do not match — nothing was changed")
		return 2
	}
	if err := vault.ChangePassphrase(old, next); err != nil {
		fmt.Fprintln(stderr, err)
		if errors.Is(err, vault.ErrWrongPassphrase) {
			return 2
		}
		return 2
	}
	_ = creds.Clear(creds.VaultCacheKey)
	fmt.Fprintf(stdout, "re-encrypted %s\n", vault.Path())
	return 0
}

// VaultRemove deletes the vault file.
func VaultRemove(stdout, stderr io.Writer, in *os.File, yes bool) int {
	if !vault.Exists() {
		fmt.Fprintf(stdout, "no vault at %s\n", vault.Path())
		return 0
	}
	if !yes {
		p := newLinePrompter(stdout, in)
		answer, err := p.line(fmt.Sprintf("Delete %s? The login in it cannot be recovered [y/N]: ", vault.Path()))
		if err != nil {
			return loginFailed(stderr, err)
		}
		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			fmt.Fprintln(stdout, "kept")
			return 0
		}
	}
	if err := vault.Remove(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	_ = creds.Clear(creds.VaultCacheKey)
	fmt.Fprintf(stdout, "removed %s\n", vault.Path())
	return 0
}

// VaultStatus says whether a vault exists, without opening it.
func VaultStatus(stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintf(stdout, "Vault:   %s\n", vault.Path())
	if !vault.Exists() {
		fmt.Fprintln(stdout, "Status:  not created  (tuta-alias vault set)")
		return 1
	}
	fmt.Fprintln(stdout, "Status:  present  (argon2id + XChaCha20-Poly1305, mode 0600)")
	fmt.Fprintf(stdout, "In use:  %v  (CRED_BACKEND=%q)\n", cfg.CredBackend == "vault", cfg.CredBackend)
	st := creds.Status(creds.VaultCacheKey, cfg.CacheTTL)
	if st.Exists {
		fmt.Fprintf(stdout, "Cached:  yes  (%s; clear it in Config → Cache status)\n", st.Mode)
	} else {
		fmt.Fprintln(stdout, "Cached:  no   (the passphrase is asked for on the next login)")
	}
	return 0
}
