package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/session"
	"github.com/tdwgm/tuta-alias/internal/vault"
)

// These tests drive the REAL Update loop. Every earlier guard asked either
// globalKey or one screen's update function directly, which is how a shortcut
// that globalKey swallowed first (Login's saved login on ^g) and a whole set
// that no screen handled (Config's ^r ^t ^x) stayed green for three releases.

// keyMsg turns a button table key ("^y", "esc", "c") into the message
// bubbletea would deliver.
func keyMsg(k string) tea.KeyMsg {
	k = strings.Replace(strings.TrimSpace(k), "^", "ctrl+", 1)
	switch k {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	}
	if strings.HasPrefix(k, "ctrl+") && len(k) == len("ctrl+")+1 {
		return tea.KeyMsg{Type: tea.KeyType(k[len("ctrl+")] - 'a' + 1)}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// The measuring instrument gets checked before it is trusted: if keyMsg built
// the wrong message, every test below would pass by not pressing anything.
func TestKeyMsgBuildsTheKeyItNames(t *testing.T) {
	for _, k := range []string{"^y", "^h", "^q", "esc", "enter", "c"} {
		want := strings.Replace(k, "^", "ctrl+", 1)
		if got := keyMsg(k).String(); got != want {
			t.Errorf("keyMsg(%q) produces %q, not %q — the key tests are measuring nothing", k, got, want)
		}
	}
}

// screenModel builds a model sitting on scr, in the state where that screen's
// menu entries are meant to be usable.
func screenModel(t *testing.T, scr screen) model {
	t.Helper()
	m := homeWithRows(t, "alpha@example.com")
	m.width, m.height = 100, 40
	m.listAll = true
	m.homeFocus = homeFocusList
	m.browseIdx = 0
	switch scr {
	case screenOptions:
		(&m).openConfig()
	case screenPlus:
		m.plusState = newPlusState("alpha@example.com", 5)
		m.cur = screenPlus
	case screenLogin:
		cfg := *m.cfg
		cfg.BWItem, cfg.BWRunItem = "item", "item"
		cfg.KeePassDB, cfg.KeePassEntry = "/tmp/db.kdbx", "tuta"
		m.cfg = &cfg
		m.loginState = newLoginState(m.cfg)
		m.loginState.savedEmail = "u@tuta.com"
		m.cur = screenLogin
	case screenHelp:
		(&m).openHelp()
	case screenDomains:
		m.domains = []string{"example.com", "example.org"}
		(&m).openDomains()
	}
	return m
}

// fingerprint is everything a key press could visibly change.
func fingerprint(m model) string {
	return fmt.Sprintf("%d|%v|%v|%d|%q|%q|%s",
		m.cur, m.confirmDelete, m.noteEdit, m.helpScroll, m.flash, m.errMsg, m.View())
}

var parityScreens = []struct {
	name string
	scr  screen
}{
	{"Home", screenHome},
	{"Login", screenLogin},
	{"Config", screenOptions},
	{"Plus", screenPlus},
	{"Help", screenHelp},
	{"Domains", screenDomains},
}

// Every key a bottom menu advertises must do something on that screen.
func TestEveryMenuShortcutActs(t *testing.T) {
	for _, sc := range parityScreens {
		for _, b := range screenModel(t, sc.scr).menuFor() {
			for _, key := range strings.Split(b.key, "/") {
				if key == "" {
					continue
				}
				t.Run(sc.name+"/"+b.label+"/"+key, func(t *testing.T) {
					m := screenModel(t, sc.scr)
					before := fingerprint(m)
					got, cmd := m.Update(keyMsg(key))
					after := fingerprint(got.(model))
					if cmd == nil && before == after {
						t.Errorf("%s: %q is advertised as %q but changed nothing", sc.name, key, b.label)
					}
				})
			}
		}
	}
}

// Enter on a menu entry must run that entry, not report that it is not wired
// up. Ten of the twenty-one entries used to do exactly that.
func TestEveryMenuEntryActs(t *testing.T) {
	for _, sc := range parityScreens {
		btns := screenModel(t, sc.scr).menuFor()
		for i, b := range btns {
			t.Run(sc.name+"/"+b.label, func(t *testing.T) {
				m := screenModel(t, sc.scr)
				m.menuActive, m.menuSel = true, i
				before := fingerprint(m)
				got, cmd := m.Update(keyMsg("enter"))
				out := got.(model)
				if strings.Contains(out.errMsg, "not wired up") {
					t.Fatalf("%s: the %q entry is not wired up", sc.name, b.label)
				}
				if cmd == nil && before == fingerprint(out) {
					t.Errorf("%s: Enter on %q changed nothing", sc.name, b.label)
				}
			})
		}
	}
}

// Opening Config must show the Config screen, not a placeholder. Every path
// that only set m.cur left optsState nil, and the first frame was a bare
// "loading..." with no banner and no rows until the next keystroke.
func TestOpeningConfigRendersRowsImmediately(t *testing.T) {
	open := map[string]func(m model) model{
		"ctrl+o": func(m model) model {
			got, _ := m.Update(keyMsg("^o"))
			return got.(model)
		},
		"menu entry": func(m model) model {
			m.menuActive = true
			for i, b := range m.menuFor() {
				if b.action == "config" {
					m.menuSel = i
				}
			}
			got, _ := m.Update(keyMsg("enter"))
			return got.(model)
		},
		"Home list row": func(m model) model {
			m.browseIdx = m.configIdx()
			got, _ := m.Update(keyMsg("enter"))
			return got.(model)
		},
	}
	for name, openIt := range open {
		t.Run(name, func(t *testing.T) {
			m := openIt(screenModel(t, screenHome))
			if m.cur != screenOptions {
				t.Fatalf("did not reach the Config screen (cur=%d)", m.cur)
			}
			if m.optsState == nil {
				t.Error("Config was opened without its state; the screen renders a placeholder")
			}
			out := m.View()
			if strings.Contains(out, "loading...") {
				t.Errorf("Config shows the placeholder instead of its rows:\n%s", out)
			}
			if !strings.Contains(out, "Default domain") {
				t.Errorf("Config is missing its first rows:\n%s", out)
			}
			// The theme group lives on its own page; the main page must
			// offer the row that opens it.
			var theme bool
			for _, r := range m.optsState.rows {
				if r.action == "open-theme" {
					theme = true
				}
			}
			if !theme {
				t.Error("the Config screen has no Theme row")
			}
		})
	}
}

// Every config key must be reachable from the Config screen: on some page,
// selectable, and with a way to change it.
func TestEveryConfigKeyIsReachable(t *testing.T) {
	m := screenModel(t, screenOptions)
	seen := map[string]string{}
	for _, page := range optsPages() {
		for _, r := range optsRowsFor(m.cfg, page) {
			if r.key != "" {
				if !r.selectable() {
					t.Errorf("%s has a row that cannot be selected", r.key)
				}
				seen[r.key] = r.action
			}
		}
	}
	for _, f := range config.Fields {
		action, ok := seen[f.Key]
		if !ok {
			t.Errorf("%s has no row on the Config screen", f.Key)
			continue
		}
		switch action {
		case "edit", "toggle", "choice", "change-domain":
		default:
			t.Errorf("%s has a row with action %q, which cannot change the value", f.Key, action)
		}
	}
}

// The Theme page opens from the Config screen and holds the whole theme
// group; the main page holds none of it.
func TestThemePageOpensFromConfig(t *testing.T) {
	m := screenModel(t, screenOptions)
	idx := -1
	for i, r := range m.optsState.rows {
		if r.action == "open-theme" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("the Config screen has no Theme row")
	}
	m.optsIdx = idx
	m, _ = m.activateOptsRow()
	if m.optsState.page != optsPageTheme {
		t.Fatal("Enter on the Theme row did not open the Theme page")
	}
	out := m.viewOptions()
	if !strings.Contains(out, "Config › Theme") {
		t.Errorf("the Theme page is not titled as a page of Config:\n%s", out)
	}
	onPage := map[string]bool{}
	for _, r := range m.optsState.rows {
		onPage[r.key] = true
	}
	for _, f := range config.Fields {
		if f.Section != config.ThemeSection() {
			continue
		}
		if !onPage[f.Key] {
			t.Errorf("%s is a theme key but is not on the Theme page", f.Key)
		}
	}
	// Esc goes back to the main page, not out of Config.
	got, _ := m.Update(keyMsg("esc"))
	m = got.(model)
	if m.cur != screenOptions || m.optsState == nil || m.optsState.page != optsPageMain {
		t.Error("Esc on the Theme page should go back to the Config page")
	}
}

// A theme key changes the theme from the Config screen, is written to the
// file, and "Reset theme" puts it back — the group the Config screen did not
// have at all, while its menu advertised a Reset that nothing implemented.
func TestThemeRowSavesAndResets(t *testing.T) {
	t.Cleanup(func() { applyStyle(config.Default()) })
	m := screenModel(t, screenOptions)
	(&m).setPage(optsPageTheme)
	idx := -1
	for i, r := range m.optsState.rows {
		if r.key == "COLOR_TEXT" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no COLOR_TEXT row on the Theme page")
	}
	m.optsIdx = idx
	m, _ = m.activateOptsRow()
	if m.optsState.state != optsChoosing {
		t.Fatal("COLOR_TEXT did not open its choice list")
	}
	m.optsState.radioIdx = indexOf(m.optsState.radio, "magenta")
	m, _ = m.updateOptions(keyMsg("enter"))
	if m.cfg.ColorText != "magenta" {
		t.Fatalf("COLOR_TEXT = %q after choosing magenta", m.cfg.ColorText)
	}
	if saved, _ := config.Load(); saved.ColorText != "magenta" {
		t.Errorf("COLOR_TEXT was not written to the file: %q", saved.ColorText)
	}
	m, _ = m.optsResetTheme()
	if m.cfg.ColorText != config.Default().ColorText {
		t.Errorf("Reset theme left COLOR_TEXT = %q", m.cfg.ColorText)
	}
	if saved, _ := config.Load(); saved.ColorText != config.Default().ColorText {
		t.Errorf("Reset theme was not written to the file: %q", saved.ColorText)
	}
}

// A value the config file would reject must be refused where it is typed,
// not accepted and silently reinterpreted later.
func TestConfigScreenRefusesInvalidValues(t *testing.T) {
	for _, c := range []struct{ key, value, want string }{
		{"PLUS_SUFFIX_LEN", "abc", "whole number"},
		{"SERVE_PORT", "70000", "port number"},
		{"SHOW_ALIASES_FOR", "[unclosed", "regexp"},
		{"TUTA_PROXY", "127.0.0.1:1055", "TUTA_PROXY"},
	} {
		t.Run(c.key, func(t *testing.T) {
			m := screenModel(t, screenOptions)
			before := config.ToMap(m.cfg)[c.key]
			m.optsIdx = optsRowIndex(t, m.optsState.rows, c.key)
			m, _ = m.activateOptsRow()
			m.optsState.input.SetValue(c.value)
			m, _ = m.updateOptions(keyMsg("enter"))
			if !strings.Contains(m.errMsg, c.want) {
				t.Errorf("errMsg = %q, want something naming %q", m.errMsg, c.want)
			}
			if got := config.ToMap(m.cfg)[c.key]; got != before {
				t.Errorf("%s was changed to %q anyway", c.key, got)
			}
			if saved, _ := config.Load(); config.ToMap(saved)[c.key] == c.value {
				t.Errorf("%s was written to the file anyway", c.key)
			}
		})
	}
}

// Help is taller than a terminal, so it has to scroll; before, everything
// below "Shortcuts" was unreachable on an 80x24 screen.
func TestHelpScrolls(t *testing.T) {
	m := screenModel(t, screenHelp)
	m.height = 24
	if m.helpMaxScroll() == 0 {
		t.Fatal("Help fits in 24 rows? then this test is measuring nothing")
	}
	first := m.View()
	got, _ := m.Update(keyMsg("down"))
	m = got.(model)
	if m.helpScroll == 0 {
		t.Fatal("down did not scroll Help")
	}
	if m.View() == first {
		t.Error("Help scrolled but rendered the same text")
	}
	// At the bottom, down falls into the bottom menu instead of doing nothing.
	m.helpScroll = m.helpMaxScroll()
	got, _ = m.Update(keyMsg("down"))
	if !got.(model).menuActive {
		t.Error("down at the end of Help did not enter the bottom menu")
	}
}

// Tab must reach the screen it is pressed on.
//
// menuNav used to toggle the bottom menu on every Tab, before any screen saw
// the key. Home could not move between its fields with it, the login form
// could not Tab from email to password, and the Plus screen's note field was
// unreachable by ANY key, because Tab was the only thing that focused it.
func TestTabMovesWithinTheScreenBeforeEnteringTheMenu(t *testing.T) {
	t.Run("home fields", func(t *testing.T) {
		m := screenModel(t, screenHome)
		m.homeFocus = homeFocusNew
		(&m).syncHomeFocus()
		got, _ := m.Update(keyMsg("tab"))
		out := got.(model)
		if out.menuActive {
			t.Fatal("Tab jumped straight into the bottom menu from the first field")
		}
		if out.homeFocus != homeFocusSearch {
			t.Errorf("Tab did not move to the Search field (focus=%d)", out.homeFocus)
		}
	})

	t.Run("login fields", func(t *testing.T) {
		m := screenModel(t, screenLogin)
		m.loginState.focus = 0
		got, _ := m.Update(keyMsg("tab"))
		out := got.(model)
		if out.menuActive || out.loginState.focus != 1 {
			t.Errorf("Tab did not move from email to password (focus=%d, menu=%v)",
				out.loginState.focus, out.menuActive)
		}
	})

	t.Run("plus note field is reachable", func(t *testing.T) {
		m := screenModel(t, screenPlus)
		if m.plusState.focus != 0 {
			t.Fatalf("the Plus screen does not start on the suffix field")
		}
		got, _ := m.Update(keyMsg("tab"))
		out := got.(model)
		if out.menuActive || out.plusState.focus != 1 {
			t.Fatalf("Tab did not reach the note field (focus=%d, menu=%v)",
				out.plusState.focus, out.menuActive)
		}
		// And the arrows do it too, because the standard moves between rows
		// with up/down and these two fields are rows.
		m2 := screenModel(t, screenPlus)
		got2, _ := m2.Update(keyMsg("down"))
		if got2.(model).plusState.focus != 1 {
			t.Error("down did not move to the note field either")
		}
	})

	t.Run("from the last row Tab enters the menu", func(t *testing.T) {
		for _, c := range []struct {
			name string
			m    func(t *testing.T) model
		}{
			{"login submit button", func(t *testing.T) model {
				m := screenModel(t, screenLogin)
				m.loginState.focus = 3
				return m
			}},
			{"plus note field", func(t *testing.T) model {
				m := screenModel(t, screenPlus)
				m.plusState.focus = 1
				return m
			}},
			{"home last list row", func(t *testing.T) model {
				m := screenModel(t, screenHome)
				m.homeFocus = homeFocusList
				m.browseIdx = m.navCount() - 1
				return m
			}},
		} {
			t.Run(c.name, func(t *testing.T) {
				got, _ := c.m(t).Update(keyMsg("tab"))
				if !got.(model).menuActive {
					t.Error("Tab on the last row did not enter the bottom menu")
				}
			})
		}
	})

	t.Run("tab leaves the menu again", func(t *testing.T) {
		m := screenModel(t, screenHome)
		m.menuActive = true
		got, _ := m.Update(keyMsg("tab"))
		if got.(model).menuActive {
			t.Error("Tab did not leave the bottom menu")
		}
	})
}

// Ticking "Save login" with CRED_BACKEND="vault" must write the vault, not
// the system keyring: the vault exists precisely for machines where the
// keyring does not, so silently saving there would save nothing usable.
func TestLoginSaveWritesTheVaultWhenItIsTheBackend(t *testing.T) {
	m := screenModel(t, screenLogin)
	cfg := *m.cfg
	cfg.CredBackend = "vault"
	m.cfg = &cfg
	m.loginState.save = true
	m.loginState.fields[0].SetValue("me@example.com")
	m.loginState.fields[1].SetValue("hunter2")

	// The form says where it will go.
	if !strings.Contains(m.viewLogin(), "encrypted vault") {
		t.Error("the Save login line still says system keyring with CRED_BACKEND=vault")
	}

	got, _ := m.Update(loginDoneMsg{sess: &session.Session{MailAddress: "me@example.com"}})
	m = got.(model)
	if m.loginState == nil || m.loginState.phase != loginPhaseVaultSave {
		t.Fatal("a successful login did not ask for the vault passphrase")
	}
	if vault.Exists() {
		t.Fatal("the vault was written before a passphrase was given")
	}

	m.loginState.masterInput.SetValue("correct horse battery staple")
	got, _ = m.Update(keyMsg("enter"))
	m = got.(model)
	if !vault.Exists() {
		t.Fatal("Enter on the passphrase prompt did not write the vault")
	}
	e, err := vault.Open("correct horse battery staple")
	if err != nil {
		t.Fatalf("the vault does not open with the passphrase just typed: %v", err)
	}
	if e.Email != "me@example.com" || e.Password != "hunter2" {
		t.Errorf("the vault holds %q / %q", e.Email, e.Password)
	}
	if m.cur != screenHome {
		t.Error("the TUI did not continue to Home after writing the vault")
	}
}

// Esc at the passphrase prompt must not undo the login, and must not leave a
// half-written vault.
func TestSkippingTheVaultSaveKeepsTheSession(t *testing.T) {
	m := screenModel(t, screenLogin)
	cfg := *m.cfg
	cfg.CredBackend = "vault"
	m.cfg = &cfg
	m.loginState.save = true
	m.loginState.fields[0].SetValue("me@example.com")
	m.loginState.fields[1].SetValue("hunter2")
	got, _ := m.Update(loginDoneMsg{sess: &session.Session{MailAddress: "me@example.com"}})
	got, _ = got.(model).Update(keyMsg("esc"))
	m = got.(model)
	if vault.Exists() {
		t.Error("Esc wrote the vault anyway")
	}
	if m.sess == nil || m.cur != screenHome {
		t.Error("Esc at the passphrase prompt threw away the session")
	}
}

// Help's backend paragraph was written out by hand and named four backends
// while CRED_BACKEND accepted five: the vault this branch added was missing,
// so the one backend a reader would not guess was the one Help did not
// mention. It comes from config.BackendNames now, which is the same list the
// Config screen and the install wizard offer.
func TestHelpNamesEveryCredentialBackend(t *testing.T) {
	help := bodyHelp()
	for _, name := range config.BackendNames {
		if name == "" {
			continue
		}
		if !strings.Contains(help, name) {
			t.Errorf("Help does not name the %q backend:\n%s", name, help)
		}
	}
}

// Help promised that Esc quits on the first screen. It does not, on purpose:
// there is no level to go back to and quitting on a stray Esc is not
// something to do to someone. q is the one that quits, so the row says that.
func TestEscOnHomeDoesNotQuitAndHelpSaysSo(t *testing.T) {
	m := homeWithRows(t, "a@example.com")
	m.cur = screenHome

	if _, cmd := press(t, m, "esc"); cmd != nil {
		t.Error("esc on Home returned a command; the row says q is what quits")
	}
	if _, cmd := press(t, m, "q"); cmd == nil {
		t.Error("q on Home did not quit")
	}

	var row string
	for _, r := range navigationRows() {
		if strings.Contains(r[0], "esc") {
			row = r[1]
		}
	}
	if !strings.Contains(row, "q quits") {
		t.Errorf("the navigation row reads %q, which does not say which key quits", row)
	}
}
