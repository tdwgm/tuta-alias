package tui

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	keyringlib "github.com/zalando/go-keyring"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/crypto"
	"github.com/tdwgm/tuta-alias/internal/login"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// gate is how a fake stays "still in flight": pass reports that a caller has
// arrived, then waits for as long as the test holds the gate. A test that
// holds it can press keys while the request is on the wire, which is the state
// the finding was about.
type gate struct {
	gateMu  sync.Mutex
	held    chan struct{}
	arrived chan struct{}
}

func newGate() *gate { return &gate{arrived: make(chan struct{}, 8)} }

func (g *gate) pass() {
	select {
	case g.arrived <- struct{}{}:
	default:
	}
	g.gateMu.Lock()
	held := g.held
	g.gateMu.Unlock()
	if held != nil {
		<-held
	}
}

// hold makes callers of pass wait until release is called. The cleanup calls
// it too, so a test that fails halfway leaves no goroutine blocked at the gate.
func (g *gate) hold(t *testing.T) (release func()) {
	t.Helper()
	held := make(chan struct{})
	g.gateMu.Lock()
	g.held = held
	g.gateMu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(held) }) }
	t.Cleanup(release)
	return release
}

// waitArrived blocks until a caller is at the gate, which is the moment the
// request is in flight.
func (g *gate) waitArrived(t *testing.T) {
	t.Helper()
	select {
	case <-g.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the fake: the command never started its request")
	}
}

// closeLog is what fakeClose records. closeSession is called from the
// goroutine a command runs on, so the list is behind a mutex.
type closeLog struct {
	*gate
	mu     sync.Mutex
	tokens []string
}

func (l *closeLog) calls() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.tokens)
}

// fakeClose answers closeSession with err and records every token it is asked
// to close, so a test can tell "a command was dispatched" from "the network was
// hit on the UI loop": the list must still be empty after Update returns.
func fakeClose(t *testing.T, err error) *closeLog {
	t.Helper()
	l := &closeLog{gate: newGate()}
	prev := closeSession
	t.Cleanup(func() { closeSession = prev })
	closeSession = func(token string) error {
		l.mu.Lock()
		l.tokens = append(l.tokens, token)
		l.mu.Unlock()
		l.pass()
		return err
	}
	return l
}

// fakeTuta answers for Tuta at the transport. The base URL is a constant, so
// the one place a test can stand in for the network is http.DefaultTransport,
// which every api.Client uses unless TUTA_PROXY set another. The session DELETE
// is answered with status once the gate lets it through; every other request
// fails at once, as it would on a machine that is offline, so the dashboard
// fetches a login starts cost nothing.
//
// It is the only fake here that does not know how the TUI is built, which is
// what lets two tests below run against the code as it was before.
type fakeTuta struct {
	*gate
	mu     sync.Mutex
	reqs   []string
	status int
}

func newFakeTuta(t *testing.T, status int) *fakeTuta {
	t.Helper()
	// A proxy that an earlier test left set would carry the request past us.
	_ = api.SetProxy("")
	f := &fakeTuta{gate: newGate(), status: status}
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = f
	return f
}

func (f *fakeTuta) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_ = r.Body.Close() // the RoundTripper contract
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	if r.Method != http.MethodDelete || !strings.HasPrefix(r.URL.Path, "/rest/sys/session/") {
		return nil, errors.New("offline")
	}
	f.pass()
	return &http.Response{
		StatusCode: f.status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    r,
	}, nil
}

func (f *fakeTuta) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

// deletes is the session DELETEs among the requests, in the order they came.
func (f *fakeTuta) deletes() []string {
	var out []string
	for _, r := range f.requests() {
		if strings.HasPrefix(r, "DELETE /rest/sys/session/") {
			out = append(out, r)
		}
	}
	return out
}

// sessionDelete is the request the API client makes to close the session
// behind token.
func sessionDelete(t *testing.T, token string) string {
	t.Helper()
	listID, elemID, err := crypto.ParseSessionID(token)
	if err != nil {
		t.Fatal(err)
	}
	return "DELETE /rest/sys/session/" + listID + "/" + elemID
}

// updateWithin runs Update on a goroutine of its own and fails the test if it
// has not returned in time. A network call made inside Update blocks the whole
// loop, so the regression would otherwise show up as a ten-minute go test
// timeout instead of a failure that names the cause.
func updateWithin(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()
	type result struct {
		m   tea.Model
		cmd tea.Cmd
	}
	done := make(chan result, 1)
	go func() {
		mm, cmd := m.Update(msg)
		done <- result{mm, cmd}
	}()
	select {
	case r := <-done:
		return r.m.(model), r.cmd
	case <-time.After(5 * time.Second):
		t.Fatalf("Update(%T) did not return in 5s: a network call is running on the UI loop", msg)
		return m, nil
	}
}

// deliver hands messages back to Update the way the program does when the
// commands that produced them finish.
func deliver(t *testing.T, m model, msgs ...tea.Msg) model {
	t.Helper()
	for _, msg := range msgs {
		m, _ = updateWithin(t, m, msg)
	}
	return m
}

// closeResults keeps the close outcomes among msgs. A login also starts the
// dashboard fetches, whose results are not what these tests are about.
func closeResults(msgs []tea.Msg) []tea.Msg {
	var out []tea.Msg
	for _, msg := range msgs {
		if _, ok := msg.(sessionClosedMsg); ok {
			out = append(out, msg)
		}
	}
	return out
}

// runningCmd is a command that is executing on a goroutine of its own, as
// bubbletea runs every command, so the test is free to press keys while a held
// close is still waiting.
type runningCmd struct{ msgs chan []tea.Msg }

func startCmd(cmd tea.Cmd) *runningCmd {
	r := &runningCmd{msgs: make(chan []tea.Msg, 1)}
	go func() { r.msgs <- runCmd(cmd) }()
	return r
}

func (r *runningCmd) wait(t *testing.T) []tea.Msg {
	t.Helper()
	select {
	case msgs := <-r.msgs:
		return msgs
	case <-time.After(5 * time.Second):
		t.Fatal("the command did not finish after the close was released")
		return nil
	}
}

// sessionToken is an access token the API client can build a session URL from.
// It is made up here, not written out, so the secret scan has nothing to read.
func sessionToken(name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("token-" + name + "-padding"))
}

func saveToken(t *testing.T, token string) {
	t.Helper()
	if _, err := session.Save(&session.Session{AccessToken: token}); err != nil {
		t.Fatal(err)
	}
}

// loggedIn is the dashboard of a user who started the TUI from a stored login:
// the session file and the model hold the same token.
func loggedIn(t *testing.T, token string) model {
	t.Helper()
	m := homeWithRows(t, "alpha@example.com")
	m.width, m.height = 100, 40
	m.cur = screenHome
	m.sess = &session.Session{MailAddress: "me@example.com", UserID: "uid", AccessToken: token, UserGroupID: "ug"}
	if _, err := session.Save(m.sess); err != nil {
		t.Fatal(err)
	}
	return m
}

// submittingLogin is the login form after the user pressed Enter, waiting for
// the result that loginDoneMsg delivers. No session is on disk yet.
func submittingLogin(t *testing.T) model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := model{cfg: config.Default(), cur: screenLogin, width: 100, height: 40}
	m.loginState = newLoginState(m.cfg)
	m.loginState.phase = loginPhaseSubmitting
	return m
}

func loginDone(token string) loginDoneMsg {
	return loginDoneMsg{sess: &session.Session{MailAddress: "me@example.com", AccessToken: token}}
}

// A logout used to call login.Logout inside Update. It sends the server its
// DELETE before it deletes the file, so the whole interface stood still for as
// long as the network took. The close is a command now: Update returns with
// the request unsent, and the command is what sends it.
func TestLogout_TheServerCloseIsACommandNotACallInUpdate(t *testing.T) {
	closes := fakeClose(t, nil)
	tok := sessionToken("A")
	m := loggedIn(t, tok)

	got, cmd := updateWithin(t, m, keyMsg("^l"))
	if cmd == nil {
		t.Fatal("^l dispatched no command")
	}
	if n := len(closes.calls()); n != 0 {
		t.Fatalf("^l closed the session %d time(s) inside Update", n)
	}
	if !strings.Contains(got.flash, "closing the previous session") {
		t.Errorf("flash = %q, want it to say the close is under way", got.flash)
	}
	if !strings.Contains(got.View(), "closing the previous session on the server") {
		t.Errorf("the screen does not say the close is under way:\n%s", got.View())
	}

	msgs := runCmd(cmd)
	if calls := closes.calls(); !slices.Equal(calls, []string{tok}) {
		t.Fatalf("the command closed %v, want [%s]", calls, tok)
	}
	got = deliver(t, got, msgs...)
	if got.flash != "logged out" {
		t.Errorf("flash = %q once the close finished, want %q", got.flash, "logged out")
	}
	if strings.Contains(got.View(), "closing") {
		t.Error("the screen still says it is closing after the result landed")
	}
}

// A logout is complete when Update returns. The local session is gone and the
// screen is the login form while the server is still being asked, and the
// close finishing, or failing, changes neither. The close is held on the wire
// for the first check, so a logout that waited for it would never get there.
func TestLogout_TheLocalLogoutIsCompleteWhateverTheServerDoes(t *testing.T) {
	cases := []struct {
		name      string
		closeErr  error
		wantFlash string
	}{
		{"the close succeeds", nil, "logged out"},
		{
			"the close fails", errors.New("HTTP 500 InternalServerError"),
			"logged out · could not close the previous session on the server: HTTP 500 InternalServerError",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			closes := fakeClose(t, c.closeErr)
			release := closes.hold(t)
			m := loggedIn(t, sessionToken("A"))
			loggedOutHere := func(t *testing.T, when string, m model) {
				t.Helper()
				if m.sess != nil {
					t.Errorf("%s: the model still holds a session", when)
				}
				if _, err := session.Load(); err == nil {
					t.Errorf("%s: the session file is still on disk", when)
				}
				if m.cur != screenLogin || m.loginState == nil {
					t.Errorf("%s: cur = %d, want the login form", when, m.cur)
				}
				if !strings.Contains(m.View(), "log in") {
					t.Errorf("%s: the view is not the login form:\n%s", when, m.View())
				}
			}

			got, cmd := updateWithin(t, m, keyMsg("^l"))
			inFlight := startCmd(cmd)
			closes.waitArrived(t) // the close is on the wire and unanswered
			loggedOutHere(t, "while the close is in flight", got)

			release()
			got = deliver(t, got, inFlight.wait(t)...)
			loggedOutHere(t, "after the close", got)
			if got.flash != c.wantFlash {
				t.Errorf("flash = %q, want %q", got.flash, c.wantFlash)
			}
			if got.errMsg != "" {
				t.Errorf("errMsg = %q: a server close that failed is a warning, the loud error is for the file", got.errMsg)
			}
		})
	}
}

// The screen answers keys while the close is on the wire: the form takes
// typing and Tab, Help opens, and none of it waits for the server.
func TestLogout_TheScreenStaysInteractiveWhileTheCloseRuns(t *testing.T) {
	closes := fakeClose(t, nil)
	release := closes.hold(t)
	m := loggedIn(t, sessionToken("A"))

	got, cmd := updateWithin(t, m, keyMsg("^l"))
	inFlight := startCmd(cmd)
	closes.waitArrived(t) // the close has begun and is being held

	got, _ = updateWithin(t, got, keyMsg("x"))
	if v := got.loginState.fields[0].Value(); v != "x" {
		t.Errorf("typing: the email field holds %q, want x", v)
	}
	got, _ = updateWithin(t, got, keyMsg("tab"))
	if got.loginState.focus != 1 {
		t.Errorf("tab: focus = %d, want the password field", got.loginState.focus)
	}
	got, _ = updateWithin(t, got, keyMsg("^h"))
	if got.cur != screenHelp {
		t.Fatalf("^h: cur = %d, want Help", got.cur)
	}
	if !strings.Contains(got.View(), "closing the previous session") {
		t.Error("Help does not say the close is still under way")
	}
	got, _ = updateWithin(t, got, keyMsg("esc"))
	if got.cur != screenLogin {
		t.Fatalf("esc: cur = %d, want the login form again", got.cur)
	}

	release()
	got = deliver(t, got, inFlight.wait(t)...)
	if got.flash != "logged out" {
		t.Errorf("flash = %q once the close finished, want %q", got.flash, "logged out")
	}
	if v := got.loginState.fields[0].Value(); v != "x" {
		t.Errorf("the result wiped what was typed meanwhile: %q", v)
	}
}

// A session file that cannot be deleted is the one failure of a logout that
// may not be quiet: the token is still on disk and the next start logs back in
// with it. It stays an error on screen whatever the server close does.
func TestLogout_ASessionFileThatCannotBeDeletedIsReported(t *testing.T) {
	fakeClose(t, nil)
	m := loggedIn(t, sessionToken("A"))
	// A non-empty directory where the file belongs: os.Remove refuses it
	// whoever runs the test, root included.
	if err := os.Remove(session.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(session.Path(), "in-the-way"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, cmd := updateWithin(t, m, keyMsg("^l"))
	want := login.ErrSessionFileRemains.Error()
	if !strings.Contains(got.errMsg, want) {
		t.Errorf("errMsg = %q, want it to say %q", got.errMsg, want)
	}
	if view := got.View(); !strings.Contains(view, "✗") || !strings.Contains(view, want) {
		t.Errorf("the error is not on screen:\n%s", view)
	}
	if got.sess != nil || got.cur != screenLogin {
		t.Errorf("sess=%v cur=%d: the screen should be the login form", got.sess != nil, got.cur)
	}

	got = deliver(t, got, runCmd(cmd)...)
	if !strings.Contains(got.errMsg, want) {
		t.Errorf("errMsg = %q after the commands finished, the error went quiet", got.errMsg)
	}
}

// The file can be read but not deleted, which the directory in the way above
// cannot show, because there the token is unreadable as well. The logout
// reports the file, and it still sends the close: the token left on disk is
// then a dead one, which is the best that can be made of it.
func TestLogout_ASessionFileThatStaysStillGetsItsSessionClosed(t *testing.T) {
	closes := fakeClose(t, nil)
	tok := sessionToken("A")
	m := loggedIn(t, tok)

	dir := filepath.Dir(session.Path())
	probe := filepath.Join(dir, "probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	// Registered after t.TempDir's own, so it runs first: the directory has to
	// be writable again for the temp dir to be removed.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := os.Remove(probe); err == nil {
		t.Skip("this user can delete from a read-only directory (root can), so the session file cannot be made to stay")
	}

	got, cmd := updateWithin(t, m, keyMsg("^l"))
	want := login.ErrSessionFileRemains.Error()
	if !strings.Contains(got.errMsg, want) {
		t.Errorf("errMsg = %q, want it to say %q", got.errMsg, want)
	}
	if n := len(closes.calls()); n != 0 {
		t.Fatalf("the logout closed the session %d time(s) inside Update", n)
	}
	msgs := runCmd(cmd)
	if calls := closes.calls(); !slices.Equal(calls, []string{tok}) {
		t.Fatalf("closed %v, want [%s]: a session file that stayed is no reason to leave the session open", calls, tok)
	}
	got = deliver(t, got, msgs...)
	if !strings.Contains(got.errMsg, want) {
		t.Errorf("errMsg = %q after the close finished, the error went quiet", got.errMsg)
	}
}

// With no token there is nothing to close, so no command pretends to close
// one and no flash says it is closing.
func TestLogout_WithNoTokenSendsNoClose(t *testing.T) {
	closes := fakeClose(t, nil)
	m := loggedIn(t, "")

	got, cmd := updateWithin(t, m, keyMsg("^l"))
	runCmd(cmd)
	if calls := closes.calls(); len(calls) != 0 {
		t.Errorf("closed %v with no token to close", calls)
	}
	if got.flash != "logged out" {
		t.Errorf("flash = %q, want %q", got.flash, "logged out")
	}
}

// renderBottom prints the error and hides the flash, so a leftover error from
// Home would hide the line that says the close is under way, and the failure
// of that close after it.
func TestLogout_AStaleErrorDoesNotHideTheCloseUnderWay(t *testing.T) {
	fakeClose(t, nil)
	m := loggedIn(t, sessionToken("A"))
	m.errMsg = "select an alias in the list first"

	got, _ := updateWithin(t, m, keyMsg("^l"))
	if got.errMsg != "" {
		t.Errorf("errMsg = %q: an error from Home followed the user to the login form", got.errMsg)
	}
	if !strings.Contains(got.View(), "closing the previous session") {
		t.Errorf("the screen does not say the close is under way:\n%s", got.View())
	}
}

// The close ends wherever the user has gone meanwhile. Help and Config are
// reachable from the login form, and no screen handler takes the result, so it
// has to be routed at the top of Update like the login results are, or the
// flash says "closing…" for good and a failure is never shown.
func TestLogout_TheCloseResultLandsWhileAnotherScreenIsOpen(t *testing.T) {
	cases := []struct {
		name string
		open string
	}{
		{"help", "^h"},
		{"config", "^o"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeClose(t, errors.New("boom"))
			m := loggedIn(t, sessionToken("A"))
			got, cmd := updateWithin(t, m, keyMsg("^l"))
			msgs := runCmd(cmd) // the close has finished by now

			got, _ = updateWithin(t, got, keyMsg(c.open))
			if got.cur == screenLogin {
				t.Fatalf("%s did not open over the login form", c.name)
			}
			got = deliver(t, got, msgs...)

			want := "logged out · could not close the previous session on the server: boom"
			if got.flash != want {
				t.Errorf("flash = %q, want %q: the result never reached the model", got.flash, want)
			}
		})
	}
}

// A login that replaces a stored session closes the old one, and that DELETE
// was sent from inside Update too (login.SaveSession). The file is still
// written there, because a failed write has to keep the user on the form; only
// the request moved.
func TestLogin_TheReplacedSessionIsClosedByACommandNotInUpdate(t *testing.T) {
	oldTok, newTok := sessionToken("old"), sessionToken("new")
	const closing = " · closing the previous session on the server…"
	cases := []struct {
		name      string
		stored    string // the token in the session file before the login
		save      bool   // "Save login" was ticked, which adds to the flash first
		wantClose []string
		wantFlash string // while the close is under way
		doneFlash string // once it has finished
	}{
		{
			"a different session was stored", oldTok, false, []string{oldTok},
			"logged in as me@example.com" + closing, "logged in as me@example.com",
		},
		{
			"the same session again", newTok, false, nil,
			"logged in as me@example.com", "logged in as me@example.com",
		},
		{
			"no session was stored", "", false, nil,
			"logged in as me@example.com", "logged in as me@example.com",
		},
		{
			// The note is found again by its place at the end of the flash, so
			// everything else the login says has to come before it.
			"a saved login is announced before the note", oldTok, true, []string{oldTok},
			"logged in as me@example.com · login saved to keyring" + closing,
			"logged in as me@example.com · login saved to keyring",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keyringlib.MockInit()
			closes := fakeClose(t, nil)
			newFakeTuta(t, http.StatusOK) // the dashboard fetches in the same batch go nowhere
			m := submittingLogin(t)
			if c.stored != "" {
				saveToken(t, c.stored)
			}
			if c.save {
				m.loginState.save = true
				m.loginState.fields[0].SetValue("me@example.com")
				m.loginState.fields[1].SetValue("secret")
			}

			got, cmd := updateWithin(t, m, loginDone(newTok))
			if cmd == nil {
				t.Fatal("the login dispatched no command, not even the dashboard fetch")
			}
			if n := len(closes.calls()); n != 0 {
				t.Fatalf("the login closed %d session(s) inside Update", n)
			}
			if s, err := session.Load(); err != nil || s.AccessToken != newTok {
				t.Errorf("the session file was not written before Update returned: %+v, %v", s, err)
			}
			if got.cur != screenHome || got.sess == nil || got.sess.AccessToken != newTok {
				t.Errorf("cur=%d sess=%v: the login should land on Home with the new session", got.cur, got.sess)
			}
			if got.flash != c.wantFlash {
				t.Errorf("flash = %q, want %q", got.flash, c.wantFlash)
			}

			msgs := runCmd(cmd)
			if calls := closes.calls(); !slices.Equal(calls, c.wantClose) {
				t.Fatalf("closed %v, want %v", calls, c.wantClose)
			}
			got = deliver(t, got, closeResults(msgs)...)
			if got.flash != c.doneFlash {
				t.Errorf("flash = %q once the close finished, want %q", got.flash, c.doneFlash)
			}
		})
	}
}

// The old session failing to close does not fail the login: the new session
// is saved and in use. The failure is still shown, because it is the one thing
// the user cannot see for themselves.
func TestLogin_AFailedCloseOfTheReplacedSessionIsAWarningNotAFailedLogin(t *testing.T) {
	oldTok, newTok := sessionToken("old"), sessionToken("new")
	fakeClose(t, errors.New("boom"))
	newFakeTuta(t, http.StatusOK)
	m := submittingLogin(t)
	saveToken(t, oldTok)

	got, cmd := updateWithin(t, m, loginDone(newTok))
	got = deliver(t, got, closeResults(runCmd(cmd))...)

	want := "logged in as me@example.com · could not close the previous session on the server: boom"
	if got.flash != want {
		t.Errorf("flash = %q, want %q", got.flash, want)
	}
	if got.errMsg != "" || got.cur != screenHome || got.sess == nil || got.sess.AccessToken != newTok {
		t.Errorf("errMsg=%q cur=%d sess=%v: the login itself should be untouched", got.errMsg, got.cur, got.sess)
	}
	if s, err := session.Load(); err != nil || s.AccessToken != newTok {
		t.Errorf("the session file = %+v, %v, want the new login", s, err)
	}
}

// Home answers keys while the old session is still closing: Tab moves the
// focus, Help opens and closes.
func TestLogin_TheDashboardStaysInteractiveWhileTheOldSessionCloses(t *testing.T) {
	oldTok, newTok := sessionToken("old"), sessionToken("new")
	closes := fakeClose(t, nil)
	release := closes.hold(t)
	newFakeTuta(t, http.StatusOK)
	m := submittingLogin(t)
	saveToken(t, oldTok)

	got, cmd := updateWithin(t, m, loginDone(newTok))
	inFlight := startCmd(cmd)
	closes.waitArrived(t) // the close has begun and is being held

	got, _ = updateWithin(t, got, keyMsg("tab"))
	if got.homeFocus != homeFocusSearch {
		t.Errorf("tab: homeFocus = %d, want the search field", got.homeFocus)
	}
	got, _ = updateWithin(t, got, keyMsg("^h"))
	if got.cur != screenHelp {
		t.Fatalf("^h: cur = %d, want Help", got.cur)
	}
	got, _ = updateWithin(t, got, keyMsg("esc"))
	if got.cur != screenHome {
		t.Fatalf("esc: cur = %d, want Home again", got.cur)
	}

	release()
	got = deliver(t, got, closeResults(inFlight.wait(t))...)
	if got.flash != "logged in as me@example.com" {
		t.Errorf("flash = %q once the close finished, want the plain login line", got.flash)
	}
}

// The vault branch of the login result returns early to ask for a passphrase.
// The close has to leave through that return as well, or a login that saves to
// the vault would leave the old session open on the server.
func TestLogin_TheCloseIsSentWhenTheVaultPassphraseIsAsked(t *testing.T) {
	oldTok, newTok := sessionToken("old"), sessionToken("new")
	closes := fakeClose(t, nil)
	m := screenModel(t, screenLogin)
	cfg := *m.cfg
	cfg.CredBackend = "vault"
	m.cfg = &cfg
	m.loginState.save = true
	m.loginState.fields[0].SetValue("me@example.com")
	m.loginState.fields[1].SetValue("hunter2")
	saveToken(t, oldTok)

	got, cmd := updateWithin(t, m, loginDone(newTok))
	if got.loginState == nil || got.loginState.phase != loginPhaseVaultSave {
		t.Fatal("the login did not ask for the vault passphrase")
	}
	if cmd == nil {
		t.Fatal("the vault branch dropped the close")
	}
	if n := len(closes.calls()); n != 0 {
		t.Fatalf("the vault branch closed %d session(s) inside Update", n)
	}
	runCmd(cmd)
	if calls := closes.calls(); !slices.Equal(calls, []string{oldTok}) {
		t.Errorf("closed %v, want [%s]", calls, oldTok)
	}
}

// The two tests below fake nothing of the TUI. They answer for Tuta at the
// transport, so they hold for the code as it was before as well, and they are
// the ones that notice a request being made on the UI loop by any route, and
// that closeSession's default really sends the DELETE for the right session.

func TestLogout_TheDeleteIsSentAfterUpdateReturnsAndTheScreenWorksMeanwhile(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			net := newFakeTuta(t, status)
			release := net.hold(t)
			tok := sessionToken("A")
			m := loggedIn(t, tok)

			got, cmd := updateWithin(t, m, keyMsg("^l"))
			if reqs := net.requests(); len(reqs) != 0 {
				t.Fatalf("Update made requests: %v", reqs)
			}
			if _, err := session.Load(); err == nil {
				t.Error("the session file is still on disk while the DELETE is on the wire")
			}
			inFlight := startCmd(cmd)
			net.waitArrived(t) // the DELETE is at the transport, unanswered

			got, _ = updateWithin(t, got, keyMsg("^h"))
			if got.cur != screenHelp {
				t.Fatalf("^h: cur = %d, want Help", got.cur)
			}
			got, _ = updateWithin(t, got, keyMsg("esc"))

			release()
			got = deliver(t, got, inFlight.wait(t)...)
			if want := []string{sessionDelete(t, tok)}; !slices.Equal(net.deletes(), want) {
				t.Errorf("DELETEs = %v, want %v", net.deletes(), want)
			}
			wantFlash := "logged out"
			if status != http.StatusOK {
				wantFlash += " · could not close the previous session on the server: HTTP 500 InternalServerError"
			}
			if got.flash != wantFlash {
				t.Errorf("flash = %q, want %q", got.flash, wantFlash)
			}
		})
	}
}

func TestLogin_TheDeleteForTheOldSessionIsSentAfterUpdateReturns(t *testing.T) {
	oldTok, newTok := sessionToken("old"), sessionToken("new")
	net := newFakeTuta(t, http.StatusOK)
	release := net.hold(t)
	m := submittingLogin(t)
	saveToken(t, oldTok)

	got, cmd := updateWithin(t, m, loginDone(newTok))
	if reqs := net.requests(); len(reqs) != 0 {
		t.Fatalf("Update made requests: %v", reqs)
	}
	inFlight := startCmd(cmd)
	net.waitArrived(t) // the DELETE is at the transport, unanswered

	got, _ = updateWithin(t, got, keyMsg("^h"))
	if got.cur != screenHelp {
		t.Fatalf("^h: cur = %d, want Help", got.cur)
	}

	release()
	inFlight.wait(t)
	if want := []string{sessionDelete(t, oldTok)}; !slices.Equal(net.deletes(), want) {
		t.Errorf("DELETEs = %v, want only the old session's %v", net.deletes(), want)
	}
}
