// Package creds provides a unified credential-backend interface for
// fetching Tuta login credentials (email + password + optional TOTP) from
// Bitwarden, KeePassXC, the bw-run wrapper, the system keyring (the
// "keyring" backend: email + password saved from the login form, never the
// TOTP; see savedlogin.go) or the built-in encrypted vault (vault.go, for
// platforms with no Secret Service). Master-passwords / session tokens are cached via
// internal/creds/keyring.go with configurable TTL.
package creds

import (
	"fmt"
	"io"

	"github.com/tdwgm/tuta-alias/internal/config"
)

// Creds holds credentials fetched from a backend. TOTP is "" when the
// backend has no TOTP field (e.g. KeePass entry without one, or
// Bitwarden item missing the authenticator field).
type Creds struct {
	Email    string
	Password string
	TOTP     string
}

// Prompter asks the user for a secret (a master password) described by
// label. It is passed per Fetch call so concurrent fetches never share a
// prompt, and a typed password lives only as long as that call.
type Prompter func(label string) (string, error)

// Backend is the abstraction every credential source implements.
type Backend interface {
	// Name returns the short identifier matching CRED_BACKEND values.
	Name() string
	// Available reports whether the backend's external tooling is installed
	// (e.g. `bw` on PATH for Bitwarden). It does not check whether config
	// is filled in.
	Available() error
	// Fetch retrieves credentials using cfg. prompt is called when a master
	// password is needed (backends without one ignore it). If verbose is
	// non-nil, the backend writes per-step progress to it (matching the
	// existing `→ <step>` style used elsewhere in the CLI).
	Fetch(cfg *config.Config, prompt Prompter, verbose io.Writer) (Creds, error)
	// ClearCache removes any cached secret for this backend.
	ClearCache(cfg *config.Config) error
}

// Resolve returns the configured backend. Returns (nil, nil) when
// CRED_BACKEND is "" (disabled). Returns an error for unknown values.
func Resolve(cfg *config.Config) (Backend, error) {
	switch cfg.CredBackend {
	case "":
		return nil, nil
	case "bitwarden":
		return bitwardenBackend{}, nil
	case "keepassxc":
		return keepassBackend{}, nil
	case "bwrun":
		return bwrunBackend{}, nil
	case "keyring":
		return keyringBackend{}, nil
	case "vault":
		return vaultBackend{}, nil
	default:
		return nil, fmt.Errorf("unknown CRED_BACKEND %q — valid values: bitwarden, keepassxc, bwrun, keyring, vault, or empty", cfg.CredBackend)
	}
}

// Fetch is a convenience wrapper that resolves + fetches in one call.
// Returns zero-value Creds with nil error when no backend is configured.
func Fetch(cfg *config.Config, prompt Prompter, verbose io.Writer) (Creds, error) {
	b, err := Resolve(cfg)
	if err != nil {
		return Creds{}, err
	}
	if b == nil {
		return Creds{}, nil
	}
	return b.Fetch(cfg, prompt, verbose)
}

func vlog(w io.Writer, msg string) {
	if w != nil {
		fmt.Fprintf(w, "→ %s\n", msg)
	}
}
