package cliflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/creds"
	"github.com/tdwgm/tuta-alias/internal/login"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// fakeBackend unlocks with master "mpw" (or needs none when cached).
type fakeBackend struct {
	cached bool
	c      creds.Creds
}

func (fakeBackend) Name() string                    { return "bitwarden" }
func (fakeBackend) Available() error                { return nil }
func (fakeBackend) ClearCache(*config.Config) error { return nil }
func (b fakeBackend) Fetch(_ *config.Config, prompt creds.Prompter, _ io.Writer) (creds.Creds, error) {
	if !b.cached {
		pw, err := prompt("master: ")
		if err != nil {
			return creds.Creds{}, err
		}
		if pw != "mpw" {
			return creds.Creds{}, errors.New("bw unlock: exit status 1\nInvalid master password.")
		}
	}
	return b.c, nil
}

// withAutoSeams installs fakes; loginTOTP is the code the fake server wants
// (0 = no 2FA). It returns a pointer to the TOTP the login was attempted with.
func withAutoSeams(t *testing.T, b creds.Backend, saved *creds.Creds, loginTOTP int) *int {
	t.Helper()
	t.Setenv("TUTA_PASSWORD", "")
	t.Setenv("TUTA_TOTP", "")
	oCfg, oRes, oSaved, oLogin, oSess := autoLoadConfig, autoResolve, autoLoadSaved, autoDoLogin, autoSaveSession
	t.Cleanup(func() {
		autoLoadConfig, autoResolve, autoLoadSaved, autoDoLogin, autoSaveSession = oCfg, oRes, oSaved, oLogin, oSess
	})
	autoLoadConfig = func() (*config.Config, error) { return &config.Config{}, nil }
	autoResolve = func(*config.Config) (creds.Backend, error) { return b, nil }
	autoLoadSaved = func() (creds.Creds, error) {
		if saved == nil {
			return creds.Creds{}, creds.ErrNoSavedLogin
		}
		return *saved, nil
	}
	used := new(int)
	autoDoLogin = func(email, pw string, totp int, cb login.TOTPCallback) (*session.Session, error) {
		if pw != "pw" {
			return nil, &api.APIError{Status: 401}
		}
		if loginTOTP != 0 {
			if totp == 0 {
				n, err := cb()
				if err != nil {
					return nil, err
				}
				totp = n
			}
			*used = totp
			if totp != loginTOTP {
				return nil, &api.APIError{Status: 401}
			}
		}
		return &session.Session{MailAddress: email}, nil
	}
	autoSaveSession = func(*session.Session) error { return nil }
	return used
}

func runAuto(t *testing.T, stdin string, opts LoginOpts) (autoResult, int) {
	t.Helper()
	var out bytes.Buffer
	code := AutoLogin(&out, io.Discard, pipeStdin(t, stdin), opts)
	var res autoResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("stdout %q is not one JSON line: %v", out.String(), err)
	}
	return res, code
}

func TestAutoLogin(t *testing.T) {
	full := creds.Creds{Email: "a@tuta.com", Password: "pw", TOTP: "123456"}
	saved := &creds.Creds{Email: "a@tuta.com", Password: "pw"}
	cases := []struct {
		name       string
		backend    creds.Backend
		saved      *creds.Creds
		loginTOTP  int
		stdin      string
		envTOTP    string
		wantStatus string
		wantSource string
		wantCode   int
	}{
		{name: "cached backend is fully automatic", backend: fakeBackend{cached: true, c: full}, loginTOTP: 123456,
			wantStatus: "ok", wantSource: "bitwarden", wantCode: 0},
		{name: "locked backend, no saved login", backend: fakeBackend{c: full},
			wantStatus: "need_master", wantSource: "bitwarden", wantCode: autoExitNeedMstr},
		{name: "locked backend, master on stdin", backend: fakeBackend{c: full}, loginTOTP: 123456, stdin: "mpw\n",
			wantStatus: "ok", wantSource: "bitwarden", wantCode: 0},
		{name: "wrong master is reported, no keyring fallback", backend: fakeBackend{c: full}, saved: saved, stdin: "nope\n",
			wantStatus: "need_master", wantSource: "bitwarden", wantCode: autoExitNeedMstr},
		{name: "locked backend falls back to saved login → need TOTP", backend: fakeBackend{c: full}, saved: saved, loginTOTP: 111111,
			wantStatus: "need_totp", wantSource: "keyring", wantCode: autoExitNeedTOTP},
		{name: "saved login + TOTP in env", backend: fakeBackend{c: full}, saved: saved, loginTOTP: 111111, envTOTP: "111111",
			wantStatus: "ok", wantSource: "keyring", wantCode: 0},
		{name: "saved login + TOTP on stdin after empty master line", backend: fakeBackend{c: full}, saved: saved, loginTOTP: 111111, stdin: "\n111111\n",
			wantStatus: "ok", wantSource: "keyring", wantCode: 0},
		{name: "wrong TOTP is rejected", backend: nil, saved: saved, loginTOTP: 111111, envTOTP: "222222",
			wantStatus: "rejected", wantSource: "keyring", wantCode: autoExitFailed},
		{name: "nothing configured", backend: nil,
			wantStatus: "need_credentials", wantCode: autoExitNeedCred},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withAutoSeams(t, tc.backend, tc.saved, tc.loginTOTP)
			t.Setenv("TUTA_TOTP", tc.envTOTP)
			res, code := runAuto(t, tc.stdin, LoginOpts{})
			if res.Status != tc.wantStatus || res.Source != tc.wantSource || code != tc.wantCode {
				t.Errorf("got (%q, %q, %d), want (%q, %q, %d); err=%q",
					res.Status, res.Source, code, tc.wantStatus, tc.wantSource, tc.wantCode, res.Error)
			}
		})
	}
}

// A form filled in by the caller (--email + $TUTA_PASSWORD) wins over every
// stored source, and --save stores it.
func TestAutoLogin_ManualSave(t *testing.T) {
	withAutoSeams(t, fakeBackend{cached: true, c: creds.Creds{Email: "other@tuta.com", Password: "x"}}, nil, 0)
	t.Setenv("TUTA_PASSWORD", "pw")
	oSave := autoSaveLogin
	t.Cleanup(func() { autoSaveLogin = oSave })
	var savedEmail string
	autoSaveLogin = func(e, _ string) error { savedEmail = e; return nil }
	res, code := runAuto(t, "", LoginOpts{Email: "me@tuta.com", Save: true})
	if res.Status != "ok" || res.Source != "manual" || code != 0 || savedEmail != "me@tuta.com" {
		t.Errorf("got %+v code=%d saved=%q", res, code, savedEmail)
	}
}
