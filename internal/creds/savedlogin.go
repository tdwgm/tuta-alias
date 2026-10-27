package creds

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	keyringlib "github.com/zalando/go-keyring"

	"github.com/tdwgm/tuta-alias/internal/config"
)

// savedLoginKey is the keyring user under service "tuta-alias" that holds a
// login saved from the TUI form. Only email + password are stored — the TOTP
// code is asked for on every login so 2FA stays a separate factor.
const savedLoginKey = "login"

// ErrNoSavedLogin is returned when nothing has been saved to the keyring.
var ErrNoSavedLogin = errors.New("no saved login in the system keyring — log in once with \"Save login\" checked")

type savedLogin struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// SaveLogin stores email + password in the system keyring (Secret Service),
// without expiry.
func SaveLogin(email, password string) error {
	data, err := json.Marshal(savedLogin{Email: email, Password: password})
	if err != nil {
		return err
	}
	if err := Cache(savedLoginKey, string(data), "forever"); err != nil {
		return fmt.Errorf("save login to keyring: %w", err)
	}
	return nil
}

// errCorruptSavedLogin marks a saved-login entry that exists but can't be used.
var errCorruptSavedLogin = errors.New("saved login in the system keyring is corrupt — delete it in Options (Cache status) and save it again")

// LoadLogin returns the saved login (TOTP always empty). It returns
// ErrNoSavedLogin only when nothing is saved; an unreachable or locked
// keyring and a corrupt entry come back as distinct, wrapped errors.
func LoadLogin() (Creds, error) {
	raw, err := getKeyringSecret(savedLoginKey)
	if errors.Is(err, keyringlib.ErrNotFound) {
		return Creds{}, ErrNoSavedLogin
	}
	if err != nil {
		return Creds{}, fmt.Errorf("load saved login: %w", err)
	}
	var s savedLogin
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Creds{}, fmt.Errorf("%w: %w", errCorruptSavedLogin, err)
	}
	if s.Email == "" {
		return Creds{}, fmt.Errorf("%w: no email address", errCorruptSavedLogin)
	}
	return Creds{Email: s.Email, Password: s.Password}, nil
}

// SavedLoginEmail returns the saved address, or "" when none is saved or the
// keyring is unreachable.
func SavedLoginEmail() string {
	c, err := LoadLogin()
	if err != nil {
		return ""
	}
	return c.Email
}

// ForgetLogin deletes the saved login.
func ForgetLogin() error { return Clear(savedLoginKey) }

// SavedLoginCacheKey is the keyring user key Options uses for status/clear.
const SavedLoginCacheKey = savedLoginKey

// keyringBackend serves the login saved from the TUI form.
type keyringBackend struct{}

func (keyringBackend) Name() string { return "keyring" }

func (keyringBackend) Available() error {
	if _, err := keyringlib.Get(keyringService, savedLoginKey); err != nil && !errors.Is(err, keyringlib.ErrNotFound) {
		return fmt.Errorf("system keyring (Secret Service) not reachable: %w", err)
	}
	return nil
}

// Fetch returns the saved login; it never needs a master password, so
// prompt is not used.
func (keyringBackend) Fetch(cfg *config.Config, prompt Prompter, verbose io.Writer) (Creds, error) {
	vlog(verbose, "keyring: load saved login")
	return LoadLogin()
}

func (keyringBackend) ClearCache(_ *config.Config) error { return ForgetLogin() }
