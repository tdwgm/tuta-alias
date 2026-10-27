package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/creds"
	"github.com/tdwgm/tuta-alias/internal/login"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/session"
	"github.com/tdwgm/tuta-alias/internal/vault"
)

// errNeedMaster is the sentinel returned by the auto-login probe's prompt
// hook: it means "no cached session, a master password is required". When
// Fetch returns this, the TUI switches to the master-password prompt instead
// of failing.
var errNeedMaster = errors.New("master password required")

// autoLoginResultMsg carries the outcome of the no-prompt auto-login probe
// dispatched from Init when a credential backend is configured.
type autoLoginResultMsg struct {
	c   creds.Creds
	err error
}

// autoLoginSkippedMsg reports that the auto-login probe did not run because
// the backend's tooling or keyring is not available.
type autoLoginSkippedMsg struct{}

// reauthInfo is the login-screen info set while re-authenticating after an
// expired session.
const reauthInfo = "session expired — re-authenticating…"

// autoLoginCmd attempts a credential fetch WITHOUT prompting — it succeeds
// only if the backend already has a valid cached/unlocked session. If a
// master password would be required, the prompt returns errNeedMaster so the
// caller can show the master-password screen. The availability check runs
// here, off the UI loop: for the keyring backend it is a D-Bus call that can
// raise an unlock dialog.
func autoLoginCmd(cfg *config.Config, b creds.Backend) tea.Cmd {
	return func() tea.Msg {
		if err := b.Available(); err != nil {
			return autoLoginSkippedMsg{}
		}
		noPrompt := func(string) (string, error) { return "", errNeedMaster }
		c, err := b.Fetch(cfg, noPrompt, nil)
		return autoLoginResultMsg{c, err}
	}
}

// savedEmailMsg carries the address of the login saved in the keyring ("" =
// none or keyring unreachable).
type savedEmailMsg struct{ email string }

// savedEmailCmd looks up the saved login's address off the UI loop (D-Bus;
// may raise a keyring unlock dialog). Dispatch it wherever a login screen is
// created.
func savedEmailCmd() tea.Cmd {
	return func() tea.Msg { return savedEmailMsg{creds.SavedLoginEmail()} }
}

// savedLoginMsg carries the saved login itself (^l).
type savedLoginMsg struct {
	c   creds.Creds
	err error
}

// savedLoginCmd reads the saved login off the UI loop — same reason as
// savedEmailCmd: the keyring call is D-Bus and may block on an unlock dialog.
func savedLoginCmd() tea.Cmd {
	return func() tea.Msg {
		c, err := creds.LoadLogin()
		return savedLoginMsg{c, err}
	}
}

// autoLoginBackend returns the configured backend to auto-login with, or nil
// when none applies (no backend, missing config, or bwrun which uses
// syscall.Exec and cannot run inside the TUI loop). Whether the tooling is
// installed is checked asynchronously by autoLoginCmd.
func autoLoginBackend(cfg *config.Config) creds.Backend {
	if cfg == nil {
		return nil
	}
	for _, name := range availableBackendNames(cfg) {
		if name != cfg.CredBackend || name == "bwrun" {
			continue
		}
		if b, err := creds.Resolve(cfg); err == nil && b != nil {
			return b
		}
	}
	return nil
}

type loginPhase int

const (
	loginPhasePicker     loginPhase = iota // choosing backend or filling manually
	loginPhaseMaster                       // entering master-pw for backend
	loginPhaseSubmitting                   // Argon2id derive + SessionService running
	loginPhaseVaultSave                    // asking for a passphrase to write the vault
)

type loginState struct {
	phase  loginPhase
	focus  int // 0=email, 1=password, 2=totp, 3=login button
	fields [3]textinput.Model

	masterInput textinput.Model
	masterLabel string
	backend     creds.Backend // selected backend while in phaseMaster

	spinner spinner.Model
	err     string
	info    string // "deriving key…" etc

	save       bool   // "Save login" checkbox: store email+password on success
	savedEmail string // address of the login saved in the keyring ("" = none)
	// pending is what a ticked "Save login" will write once the vault
	// passphrase has been typed (CRED_BACKEND="vault").
	pending creds.Creds
}

func newLoginState(cfg *config.Config) *loginState {
	// savedEmail is filled in asynchronously by savedEmailCmd.
	s := &loginState{phase: loginPhasePicker}
	emailI := textinput.New()
	emailI.Placeholder = "you@tuta.com"
	emailI.CharLimit = 128
	emailI.Width = 32
	emailI.Focus()
	s.fields[0] = emailI

	pwI := textinput.New()
	pwI.Placeholder = "password"
	pwI.EchoMode = textinput.EchoPassword
	pwI.CharLimit = 256
	pwI.Width = 32
	s.fields[1] = pwI

	totpI := textinput.New()
	totpI.Placeholder = "6-digit code (optional)"
	totpI.CharLimit = 6
	totpI.Width = 8
	s.fields[2] = totpI

	mI := textinput.New()
	mI.EchoMode = textinput.EchoPassword
	mI.CharLimit = 256
	mI.Width = 32
	s.masterInput = mI

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	s.spinner = sp

	return s
}

// availableBackendNames returns config-configured backend names. The TUI
// only shows buttons for backends whose required config is filled in.
func availableBackendNames(cfg *config.Config) []string {
	var out []string
	if cfg == nil {
		return out
	}
	if cfg.BWItem != "" {
		out = append(out, "bitwarden")
	}
	if cfg.KeePassDB != "" && cfg.KeePassEntry != "" {
		out = append(out, "keepassxc")
	}
	if cfg.BWRunItem != "" {
		out = append(out, "bwrun")
	}
	if cfg.CredBackend == "keyring" {
		out = append(out, "keyring")
	}
	// The vault is "configured" when the file exists: it has no config key of
	// its own, and auto-login needs to see it here or it never starts.
	if cfg.CredBackend == "vault" && vault.Exists() {
		out = append(out, "vault")
	}
	return out
}

// backendConfigured reports whether name's required config is filled in —
// the same condition that shows its button on the login screen.
func backendConfigured(cfg *config.Config, name string) bool {
	for _, n := range availableBackendNames(cfg) {
		if n == name {
			return true
		}
	}
	return false
}

// fillSavedLogin puts a keyring login into the form and parks the cursor on
// the TOTP field: the code is never saved, so the user types it and hits Enter.
func (s *loginState) fillSavedLogin(c creds.Creds) {
	s.fields[0].SetValue(c.Email)
	s.fields[1].SetValue(c.Password)
	s.fields[0].Blur()
	s.fields[1].Blur()
	s.focus = 2
	s.fields[2].Focus()
	s.err = ""
	s.info = ""
	s.phase = loginPhasePicker
}

// loginDoneMsg is sent when login.Login returns from the goroutine.
type loginDoneMsg struct {
	sess *session.Session
	err  error
}

// credsDoneMsg is sent when creds.Fetch returns from the goroutine. It
// auto-fills the form and triggers login.
type credsDoneMsg struct {
	c   creds.Creds
	err error
}

func (m model) updateLogin(msg tea.Msg) (model, tea.Cmd) {
	if m.loginState == nil {
		m.loginState = newLoginState(m.cfg)
		mm, cmd := m.updateLogin(msg)
		return mm, tea.Batch(cmd, savedEmailCmd())
	}
	s := m.loginState

	switch msg := msg.(type) {
	case loginDoneMsg:
		s.phase = loginPhasePicker
		if msg.err != nil {
			if errors.Is(msg.err, login.ErrTOTPRequired) {
				s.err = "2FA required — enter TOTP and retry"
			} else {
				s.err = msg.err.Error()
			}
			logx.Errorf("login: %v", msg.err)
			return m, nil
		}
		logx.Infof("logged in as %s", msg.sess.MailAddress)
		// The file is written here, on the UI loop. It is local, a failed write
		// has to keep the user on this form with the reason, and a session that
		// the server holds but this machine never recorded is one nobody can
		// resume or close. Closing the session it replaces is a network call,
		// so that part is a command (see sessionclose.go).
		_, replaced, err := login.ReplaceSession(msg.sess)
		if err != nil {
			s.err = "save session: " + err.Error()
			return m, nil
		}
		m.sess = msg.sess
		m.flash = "logged in as " + msg.sess.MailAddress
		email, password := strings.TrimSpace(s.fields[0].Value()), s.fields[1].Value()
		if s.save && m.cfg.CredBackend == "vault" {
			// The vault needs a passphrase of its own, so the save cannot
			// finish here. Stay on this screen and ask for it; the session is
			// already good either way.
			s.pending = creds.Creds{Email: email, Password: password}
			s.masterLabel = "New vault passphrase (the login is saved encrypted)"
			s.masterInput.Reset()
			s.masterInput.Focus()
			s.phase = loginPhaseVaultSave
			closeCmd := (&m).beginClose(replaced)
			return m, closeCmd
		}
		m.cur = screenHome
		if s.save {
			if err := creds.SaveLogin(email, password); err != nil {
				m.errMsg = err.Error()
				logx.Errorf("save login: %v", err)
			} else {
				m.flash += " · login saved to keyring"
			}
		}
		m.loginState = nil
		closeCmd := (&m).beginClose(replaced)
		return m, tea.Batch(m.loadHomeCmd(), closeCmd)
	case savedLoginMsg:
		s.info = ""
		if msg.err != nil {
			s.err = msg.err.Error()
			return m, nil
		}
		s.fillSavedLogin(msg.c)
		return m, nil
	case credsDoneMsg:
		if msg.err != nil {
			s.phase = loginPhasePicker
			s.err = msg.err.Error()
			return m, nil
		}
		s.fields[0].SetValue(msg.c.Email)
		s.fields[1].SetValue(msg.c.Password)
		s.fields[2].SetValue(msg.c.TOTP)
		return m, m.startLoginCmd()
	case autoLoginSkippedMsg:
		if s.info == reauthInfo {
			s.info = ""
			s.err = "session expired — please log in again"
		}
		return m, nil
	case autoLoginResultMsg:
		if msg.err == nil && m.cfg.CredBackend == "keyring" {
			s.fillSavedLogin(msg.c)
			return m, nil
		}
		if msg.err == nil {
			// Cached session worked → fill form and log in, no prompt.
			s.fields[0].SetValue(msg.c.Email)
			s.fields[1].SetValue(msg.c.Password)
			s.fields[2].SetValue(msg.c.TOTP)
			return m, m.startLoginCmd()
		}
		if errors.Is(msg.err, errNeedMaster) {
			// No cached session → go straight to the master-password prompt.
			if b, err := creds.Resolve(m.cfg); err == nil && b != nil {
				s.backend = b
				s.masterLabel = b.Name() + " master password"
				s.masterInput.Focus()
				s.phase = loginPhaseMaster
			}
			return m, nil
		}
		// Real error (e.g. bw locked-out, item missing) → fall back to form.
		s.phase = loginPhasePicker
		s.err = msg.err.Error()
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return m, cmd
	}

	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return m, nil
	}

	switch s.phase {
	case loginPhaseMaster:
		switch k.String() {
		case "esc":
			s.phase = loginPhasePicker
			s.masterInput.Reset()
			return m, nil
		case "enter":
			pw := s.masterInput.Value()
			s.masterInput.Reset()
			b := s.backend
			cfg := m.cfg
			s.phase = loginPhaseSubmitting
			s.info = "fetching credentials from " + b.Name() + "…"
			return m, tea.Batch(
				s.spinner.Tick,
				func() tea.Msg {
					typed := func(string) (string, error) { return pw, nil }
					c, err := b.Fetch(cfg, typed, nil)
					return credsDoneMsg{c, err}
				},
			)
		}
		var cmd tea.Cmd
		s.masterInput, cmd = s.masterInput.Update(msg)
		return m, cmd

	case loginPhaseVaultSave:
		switch k.String() {
		case "esc":
			// Skipping the save does not undo the login.
			s.phase = loginPhasePicker
			s.masterInput.Reset()
			s.pending = creds.Creds{}
			m.cur = screenHome
			m.loginState = nil
			m.flash = "logged in · vault not written"
			return m, m.loadHomeCmd()
		case "enter":
			pass := s.masterInput.Value()
			s.masterInput.Reset()
			if pass == "" {
				s.err = "the passphrase must not be empty"
				return m, nil
			}
			entry := vault.Entry{Email: s.pending.Email, Password: s.pending.Password}
			s.pending = creds.Creds{}
			if err := vault.Save(pass, entry); err != nil {
				s.err = err.Error()
				logx.Errorf("vault save: %v", err)
				return m, nil
			}
			// A passphrase cached for the old vault would be wrong now.
			_ = creds.Clear(creds.VaultCacheKey)
			logx.Infof("vault written for %s", entry.Email)
			m.cur = screenHome
			m.loginState = nil
			m.flash = "logged in · login saved to the encrypted vault"
			return m, m.loadHomeCmd()
		}
		var cmd tea.Cmd
		s.masterInput, cmd = s.masterInput.Update(msg)
		return m, cmd

	case loginPhaseSubmitting:
		if k.String() == "esc" {
			// User can't actually cancel a goroutine; this just hides the spinner.
			// Real cancellation would require ctx-aware Fetch/Login.
			return m, nil
		}
		return m, nil
	}

	// Picker / manual form phase.
	switch k.String() {
	case "esc":
		// Esc from login: if no session, quit the program. Otherwise back to Home.
		if m.sess == nil {
			return m, tea.Quit
		}
		m.cur = screenHome
		m.loginState = nil
		return m, nil
	case "tab", "shift+tab", "down", "up":
		dir := 1
		if k.String() == "shift+tab" || k.String() == "up" {
			dir = -1
		}
		s.focus = (s.focus + dir + 4) % 4
		s.fields[0].Blur()
		s.fields[1].Blur()
		s.fields[2].Blur()
		if s.focus < 3 {
			s.fields[s.focus].Focus()
		}
		return m, nil
	case "ctrl+b", "ctrl+k", "ctrl+r":
		// Only the backends whose button is shown (config filled in) react.
		// An unconfigured one falls through to the field instead of being
		// swallowed: ^b and ^k are the field's own keys (cursor-left and
		// kill-to-end), and taking them for a shortcut that does nothing is
		// the worst of both.
		name := map[string]string{"ctrl+b": "bitwarden", "ctrl+k": "keepassxc", "ctrl+r": "bwrun"}[k.String()]
		if backendConfigured(m.cfg, name) {
			return m.startBackendFetch(name)
		}
	case "ctrl+s":
		s.save = !s.save
		return m, nil
	case "ctrl+l":
		// Was ^g until 0.12: globalKey takes ^g for Help before updateLogin
		// ever runs, so the saved-login shortcut had been dead since Help got
		// that key. ^l is free of bubbles' textinput keymap and unused here.
		return m.startSavedLogin()
	case "enter":
		if s.focus == 3 || s.focus == 2 {
			return m, m.startLoginCmd()
		}
		s.focus++
		s.fields[s.focus-1].Blur()
		if s.focus < 3 {
			s.fields[s.focus].Focus()
		}
		return m, nil
	}

	if s.focus < 3 {
		var cmd tea.Cmd
		s.fields[s.focus], cmd = s.fields[s.focus].Update(msg)
		return m, cmd
	}
	return m, nil
}

// startSavedLogin reads the login saved in the keyring (^l and the "Saved
// login" menu entry). The read is a D-Bus call, so it runs off the UI loop.
func (m model) startSavedLogin() (model, tea.Cmd) {
	s := m.loginState
	if s == nil {
		return m, nil
	}
	s.err = ""
	s.info = "reading the saved login…"
	return m, savedLoginCmd()
}

// startBackendFetch resolves the named backend and dispatches creds.Fetch.
// If the backend requires a master password, the TUI first switches to
// phaseMaster to collect it; otherwise it runs Fetch immediately.
func (m model) startBackendFetch(name string) (model, tea.Cmd) {
	s := m.loginState
	b, err := creds.Resolve(&config.Config{CredBackend: name})
	if err != nil || b == nil {
		s.err = "backend not available"
		return m, nil
	}
	if err := b.Available(); err != nil {
		s.err = err.Error()
		return m, nil
	}
	// Pre-fill from config so Fetch can find the item.
	mergedCfg := *m.cfg
	mergedCfg.CredBackend = name
	m.cfg = &mergedCfg

	switch name {
	case "bitwarden", "keepassxc":
		// Master password needed unless cached.
		s.backend = b
		s.masterLabel = b.Name() + " master password"
		s.masterInput.Focus()
		s.phase = loginPhaseMaster
		return m, nil
	case "bwrun":
		// bwrun uses syscall.Exec — can't be called from inside a TUI loop.
		s.err = "bwrun must be invoked from CLI: tuta-alias login --bw " + m.cfg.BWRunItem
		return m, nil
	}
	return m, nil
}

// startLoginCmd reads form fields and dispatches login.Login in a goroutine.
func (m model) startLoginCmd() tea.Cmd {
	s := m.loginState
	email := strings.TrimSpace(s.fields[0].Value())
	pw := s.fields[1].Value()
	totp := 0
	if v := strings.TrimSpace(s.fields[2].Value()); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			totp = n
		}
	}
	if email == "" || pw == "" {
		s.err = "email and password required"
		return nil
	}
	s.err = ""
	s.info = "deriving Argon2id key (~2s)…"
	s.phase = loginPhaseSubmitting
	c := api.NewClient(consts.BaseURL, "")
	return tea.Batch(
		s.spinner.Tick,
		func() tea.Msg {
			sess, err := login.Login(c, email, pw, totp, nil)
			return loginDoneMsg{sess, err}
		},
	)
}

// errLine renders a backend error in the single line the form has for it.
// The credential CLIs answer in paragraphs — bw prints a header and then one
// bare id per matching item, keepassxc prints usage — and a paragraph pasted
// into the frame was cut off at the right edge, which is where the useful
// half of the sentence lives. The text is folded onto one logical line and
// then wrapped to the terminal.
func errLine(msg string) string {
	parts := strings.FieldsFunc(msg, func(r rune) bool { return r == '\n' || r == '\r' })
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return flashLine(termWidth, msgErr, strings.Join(parts, " · "))
}

func (m model) viewLogin() string {
	if m.loginState == nil {
		return titleStyle.Render("tuta-alias — log in") + "\n" + mutedStyle.Render("loading…") + "\n"
	}
	s := m.loginState
	var b strings.Builder
	b.WriteString(m.banner())
	b.WriteString("\n")
	b.WriteString(titleStyle.Render("tuta-alias — log in"))
	b.WriteString("\n\n")

	if s.phase == loginPhaseMaster || s.phase == loginPhaseVaultSave {
		b.WriteString(indent + txt(s.masterLabel) + ":\n  " + s.masterInput.View() + "\n\n")
		hint := "Enter to submit  ·  Esc to log in manually (email/password/TOTP)"
		if s.phase == loginPhaseVaultSave {
			hint = "Enter to write the vault  ·  Esc to skip (you stay logged in)"
		}
		if s.err != "" {
			b.WriteString(errLine(s.err) + "\n\n")
		}
		b.WriteString(mutedStyle.Render(hint))
		b.WriteString("\n")
		return b.String()
	}

	// Backend buttons (only those with relevant config filled).
	var buttons []string
	if backendConfigured(m.cfg, "bitwarden") {
		buttons = append(buttons, "Ctrl+B: Bitwarden ("+m.cfg.BWItem+")")
	}
	if backendConfigured(m.cfg, "keepassxc") {
		buttons = append(buttons, "Ctrl+K: KeePassXC ("+m.cfg.KeePassEntry+")")
	}
	if backendConfigured(m.cfg, "bwrun") {
		buttons = append(buttons, "Ctrl+R: bw-run ("+m.cfg.BWRunItem+")")
	}
	if s.savedEmail != "" {
		buttons = append(buttons, "Ctrl+L: saved login ("+s.savedEmail+")")
	}
	if len(buttons) > 0 {
		b.WriteString(mutedStyle.Render("Use a credential backend:"))
		b.WriteString("\n")
		for _, btn := range buttons {
			b.WriteString("  " + keyStyle.Render(btn) + "\n")
		}
		b.WriteString("\n")
	} else {
		b.WriteString(mutedStyle.Render("(no credential backend configured — set one in Options)"))
		b.WriteString("\n\n")
	}

	labels := []string{"Email:   ", "Password:", "TOTP:    ", ""}
	for i := 0; i < 3; i++ {
		marker := "  "
		if s.focus == i {
			marker = iconStyle.Render("▶ ")
		}
		b.WriteString(fmt.Sprintf("%s%s %s\n", marker, txt(labels[i]), s.fields[i].View()))
	}
	box := "[ ]"
	if s.save {
		box = iconStyle.Render("[x]")
	}
	// Where it goes depends on the backend, so the line has to say which.
	where := "system keyring"
	if m.cfg.CredBackend == "vault" {
		where = "the encrypted vault"
	}
	b.WriteString("  " + box + " Save login to " + where + " " +
		mutedStyle.Render("(Ctrl+S · email + password, never TOTP)") + "\n")
	// Submit row.
	submitMark := "  "
	if s.focus == 3 {
		submitMark = iconStyle.Render("▶ ")
	}
	b.WriteString(submitMark + bracketStyle.Render("[") + keyStyle.Render("Login") + bracketStyle.Render("]"))
	if s.phase == loginPhaseSubmitting {
		b.WriteString("  " + s.spinner.View() + " " + mutedStyle.Render(s.info))
	}
	b.WriteString("\n\n")

	// s.err is the login form's own error and is not in m.errMsg, so it is
	// printed here; renderBottom then adds the menu (and any model-level
	// message) below it.
	if s.err != "" {
		b.WriteString(errLine(s.err))
		b.WriteString("\n")
	}
	b.WriteString(m.renderBottom())
	b.WriteString("\n")
	return b.String()
}
