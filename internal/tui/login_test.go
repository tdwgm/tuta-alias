package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	keyringlib "github.com/zalando/go-keyring"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/creds"
	"github.com/tdwgm/tuta-alias/internal/login"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// Backend shortcuts only act when their button is shown.
func TestLogin_BackendKeysRequireConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no bw / keepassxc-cli / bw-run
	cases := []struct {
		name string
		key  tea.KeyType
		cfg  *config.Config
		acts bool
	}{
		{"ctrl+b unconfigured", tea.KeyCtrlB, &config.Config{}, false},
		{"ctrl+k unconfigured", tea.KeyCtrlK, &config.Config{KeePassDB: "db.kdbx"}, false},
		{"ctrl+r unconfigured", tea.KeyCtrlR, &config.Config{}, false},
		{"ctrl+b configured", tea.KeyCtrlB, &config.Config{BWItem: "tuta"}, true},
		{"ctrl+k configured", tea.KeyCtrlK, &config.Config{KeePassDB: "db.kdbx", KeePassEntry: "tuta"}, true},
		{"ctrl+r configured", tea.KeyCtrlR, &config.Config{BWRunItem: "tuta"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := model{cfg: c.cfg, cur: screenLogin}
			m.loginState = newLoginState(m.cfg)
			got, _ := m.updateLogin(tea.KeyMsg{Type: c.key})
			// With tooling absent, a configured backend reports why it can't
			// run; an unconfigured one must stay silent.
			if acted := got.loginState.err != ""; acted != c.acts {
				t.Errorf("acted = %v (err %q), want %v", acted, got.loginState.err, c.acts)
			}
		})
	}
}

// The footer says "Esc back" when Esc returns to Home instead of quitting.
// Esc means "back one level, quit on the first screen" (TUI Scaffold B4).
// Login IS the first screen when there is no session, so the bottom menu must
// offer Quit there and Back once a session exists. This used to be a sentence
// in a hand-written hint line; it is now an entry in the generated menu, and
// the assertion moved with it rather than being deleted.
func TestLogin_MenuBackEntryDependsOnSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cases := []struct {
		name       string
		sess       *session.Session
		want, gone string
	}{
		{"no session", nil, "Quit ^q", "Back esc"},
		{"session", &session.Session{}, "Back esc", "Quit ^q"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := model{cfg: config.Default(), cur: screenLogin, sess: c.sess, width: 100}
			m.loginState = newLoginState(m.cfg)
			out := m.viewLogin()
			if !strings.Contains(out, c.want) {
				t.Errorf("login menu missing %q:\n%s", c.want, out)
			}
			if strings.Contains(out, c.gone) {
				t.Errorf("login menu should not offer %q with sess=%v", c.gone, c.sess != nil)
			}
		})
	}
}

// runCmd executes cmd and flattens tea.Batch results into their messages.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		out = append(out, runCmd(c)...)
	}
	return out
}

func hasSavedEmailMsg(msgs []tea.Msg, want string) bool {
	for _, msg := range msgs {
		if se, ok := msg.(savedEmailMsg); ok && se.email == want {
			return true
		}
	}
	return false
}

// The keyring is never read on the UI loop: creating a login screen leaves
// savedEmail empty and every path that creates one dispatches savedEmailCmd.
func TestLogin_SavedEmailLookupIsAsync(t *testing.T) {
	keyringlib.MockInit()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := creds.SaveLogin("u@tuta.com", "pw"); err != nil {
		t.Fatal(err)
	}
	if s := newLoginState(config.Default()); s.savedEmail != "" {
		t.Fatalf("newLoginState read the keyring synchronously: %q", s.savedEmail)
	}

	cases := []struct {
		name string
		run  func() (model, tea.Cmd)
	}{
		{"Init without session", func() (model, tea.Cmd) {
			m := model{cfg: config.Default(), cur: screenLogin, loginState: newLoginState(config.Default())}
			return m, m.Init()
		}},
		{"Init with keyring backend", func() (model, tea.Cmd) {
			cfg := &config.Config{CredBackend: "keyring"}
			m := model{cfg: cfg, cur: screenLogin, loginState: newLoginState(cfg)}
			return m, m.Init()
		}},
		{"ctrl+l logout", func() (model, tea.Cmd) {
			m := model{cfg: config.Default(), sess: &session.Session{}}
			m.ensureHome()
			return m.updateHome(tea.KeyMsg{Type: tea.KeyCtrlL})
		}},
		{"expired session", func() (model, tea.Cmd) {
			m := model{cfg: config.Default(), sess: &session.Session{}}
			mm, cmd, _ := m.routeIfExpired(&api.APIError{Status: 440})
			return mm, cmd
		}},
		{"lazy login state", func() (model, tea.Cmd) {
			m := model{cfg: config.Default(), cur: screenLogin}
			return m.updateLogin(key("a"))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, cmd := c.run()
			if m.loginState == nil {
				t.Fatal("no login state")
			}
			if !hasSavedEmailMsg(runCmd(cmd), "u@tuta.com") {
				t.Error("savedEmailCmd not dispatched")
			}
		})
	}
}

// The keyring backend's Available() (a D-Bus call) must not run on the UI
// loop; autoLoginBackend decides from config alone.
func TestAutoLogin_KeyringAvailabilityCheckedInCmd(t *testing.T) {
	t.Cleanup(keyringlib.MockInit)
	keyringlib.MockInitWithError(errors.New("dbus: no secret service"))
	cfg := &config.Config{CredBackend: "keyring"}
	b := autoLoginBackend(cfg)
	if b == nil {
		t.Fatal("keyring backend should be selected from config alone")
	}
	msgs := runCmd(autoLoginCmd(cfg, b))
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	if _, ok := msgs[0].(autoLoginSkippedMsg); !ok {
		t.Fatalf("unreachable keyring should skip auto-login, got %T", msgs[0])
	}
	// After an expired session the skip turns into the manual-login hint.
	m := model{cfg: cfg, cur: screenLogin, loginState: newLoginState(cfg)}
	m.loginState.info = reauthInfo
	m, _ = m.updateLogin(msgs[0])
	if m.loginState.err != "session expired — please log in again" || m.loginState.info != "" {
		t.Errorf("err=%q info=%q", m.loginState.err, m.loginState.info)
	}
}

// A TOTP-required error wrapped by a caller is still recognised.
func TestLogin_WrappedTOTPRequired(t *testing.T) {
	m := model{cfg: config.Default(), cur: screenLogin, loginState: newLoginState(config.Default())}
	m, _ = m.updateLogin(loginDoneMsg{err: fmt.Errorf("login: %w", login.ErrTOTPRequired)})
	if !strings.Contains(m.loginState.err, "2FA required") {
		t.Errorf("err = %q", m.loginState.err)
	}
}

// A login result must land whatever screen is on top. Help, Config, Doctor
// and Logs are reachable without a session, so the no-session guard in Update
// deliberately leaves `cur` alone while one of them is open; before the fix
// the switch below the guard had nowhere to send loginDoneMsg (screenHelp has
// no case; updateOptions and updateDiag return on anything that is not a
// key), so the result was dropped: the session was never saved and the form
// stayed in loginPhaseSubmitting, where every key is ignored and only ^q
// gets out. Driven through the real Update loop, including the ^h that opens
// Help.
func TestLogin_ResultLandsWhileAnotherScreenIsOpen(t *testing.T) {
	sess := &session.Session{
		MailAddress: "user@example.com",
		UserID:      "uid",
		AccessToken: "tok",
		UserGroupID: "ug",
	}
	cases := []struct {
		name string
		open tea.KeyMsg
	}{
		{"help", tea.KeyMsg{Type: tea.KeyCtrlH}},
		{"config", tea.KeyMsg{Type: tea.KeyCtrlO}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			m := model{cfg: &config.Config{}, cur: screenLogin}
			m.loginState = newLoginState(m.cfg)
			m.loginState.phase = loginPhaseSubmitting

			// Open the screen on top of the form the way a user does.
			mm, _ := m.Update(c.open)
			m = mm.(model)
			if m.cur == screenLogin {
				t.Fatalf("%s did not open anything over the login form", c.name)
			}

			// The login goroutine returns while that screen is up.
			mm, _ = m.Update(loginDoneMsg{sess: sess})
			m = mm.(model)

			if m.sess == nil {
				t.Fatal("session dropped: the login result never reached updateLogin")
			}
			if m.sess.MailAddress != sess.MailAddress {
				t.Errorf("session = %q, want %q", m.sess.MailAddress, sess.MailAddress)
			}
			if got, err := session.Load(); err != nil || got == nil {
				t.Errorf("session not saved to disk: %v", err)
			}
			if m.loginState != nil && m.loginState.phase == loginPhaseSubmitting {
				t.Error("form still in loginPhaseSubmitting, every key would be ignored")
			}
		})
	}
}

// A credential backend's error has to be readable in the one line the form
// gives it. bw answers an ambiguous item with a header and then one bare id
// per match, which was pasted into the frame as-is: the sentence that says
// what went wrong was cut off at the right edge and the ids took four rows.
func TestLogin_BackendErrorIsFoldedAndWrapped(t *testing.T) {
	old := termWidth
	t.Cleanup(func() { termWidth = old })
	const width = 61
	termWidth = width

	raw := "bw get username: More than one result was found. " +
		"Try getting a specific object by id:\n" +
		"fd3bfc5e-036f-42f6-a77f-b4d700bdc830\n" +
		"8f8d52cc-41ec-4616-ab8c-b4d300c64a2b\n"
	out := errLine(raw)

	for _, want := range []string{"More than one result", "fd3bfc5e", "8f8d52cc"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q was dropped from the error:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if n := len([]rune(ansi.Strip(line))); n > width {
			t.Errorf("a %d-column line on a %d-column terminal:\n  %s", n, width, line)
		}
	}
	if strings.Count(out, "✗") != 1 {
		t.Errorf("want one error mark, got %d:\n%s", strings.Count(out, "✗"), out)
	}
}
