package creds

import (
	"errors"
	"testing"

	keyringlib "github.com/zalando/go-keyring"

	"github.com/tdwgm/tuta-alias/internal/config"
)

func TestSavedLogin_RoundTripAndForget(t *testing.T) {
	keyringlib.MockInit()
	if _, err := LoadLogin(); !errors.Is(err, ErrNoSavedLogin) {
		t.Fatalf("empty keyring: want ErrNoSavedLogin, got %v", err)
	}
	if err := SaveLogin("u@tuta.com", "pw"); err != nil {
		t.Fatal(err)
	}
	c, err := LoadLogin()
	if err != nil || c.Email != "u@tuta.com" || c.Password != "pw" || c.TOTP != "" {
		t.Fatalf("LoadLogin = %+v, %v", c, err)
	}
	if got := SavedLoginEmail(); got != "u@tuta.com" {
		t.Errorf("SavedLoginEmail = %q", got)
	}
	b, err := Resolve(&config.Config{CredBackend: "keyring"})
	if err != nil || b == nil || b.Name() != "keyring" || b.Available() != nil {
		t.Fatalf("Resolve keyring: %v %v", b, err)
	}
	if err := ForgetLogin(); err != nil {
		t.Fatal(err)
	}
	if SavedLoginEmail() != "" {
		t.Error("login still present after ForgetLogin")
	}
}

// Only "nothing saved" is ErrNoSavedLogin; keyring failures and corrupt
// entries must surface as their own errors.
func TestLoadLogin_DistinguishesErrors(t *testing.T) {
	dbusErr := errors.New("dbus: org.freedesktop.secrets not provided")
	cases := []struct {
		name      string
		setup     func(t *testing.T)
		wantNoSav bool
		wantIs    error
	}{
		{name: "nothing saved", setup: func(*testing.T) { keyringlib.MockInit() }, wantNoSav: true},
		{name: "keyring unreachable", setup: func(*testing.T) { keyringlib.MockInitWithError(dbusErr) }, wantIs: dbusErr},
		{name: "entry not JSON", setup: func(t *testing.T) {
			keyringlib.MockInit()
			if err := keyringlib.Set(keyringService, savedLoginKey, "garbage"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "login payload not JSON", setup: func(t *testing.T) {
			keyringlib.MockInit()
			if err := Cache(savedLoginKey, "garbage", "forever"); err != nil {
				t.Fatal(err)
			}
		}, wantIs: errCorruptSavedLogin},
		{name: "login without email", setup: func(t *testing.T) {
			keyringlib.MockInit()
			if err := Cache(savedLoginKey, `{"password":"pw"}`, "forever"); err != nil {
				t.Fatal(err)
			}
		}, wantIs: errCorruptSavedLogin},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Cleanup(keyringlib.MockInit)
			c.setup(t)
			_, err := LoadLogin()
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, ErrNoSavedLogin) != c.wantNoSav {
				t.Errorf("errors.Is(ErrNoSavedLogin) = %v, want %v (err %v)", !c.wantNoSav, c.wantNoSav, err)
			}
			if c.wantIs != nil && !errors.Is(err, c.wantIs) {
				t.Errorf("err %v does not wrap %v", err, c.wantIs)
			}
			if got := SavedLoginEmail(); got != "" {
				t.Errorf("SavedLoginEmail = %q, want empty on error", got)
			}
		})
	}
}
