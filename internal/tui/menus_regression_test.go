package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/notes"
	"github.com/tdwgm/tuta-alias/internal/plusalias"
)

// Regressions from the code review of the TUI Scaffold port, 2026-10-26. Both
// critical ones were introduced by the bottom menu and were found by driving
// the model, not by reading it, so they are pinned the same way.

// h must be a character inside Home's two modals. They open from
// homeFocusList, so focus alone does not say whether a key is typed, and
// inTextField() originally only knew the two dashboard fields.
//
// The consequence was not cosmetic: the delete confirmation requires typing
// the address exactly, so every alias containing an h was undeletable.
func TestHInHomeModalsIsTypedNotHelp(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(m *model)
	}{
		{"delete confirmation", func(m *model) { m.confirmDelete = true; m.deleteTarget = "hallon@example.com" }},
		{"note editor", func(m *model) { m.noteEdit = true; m.noteTarget = "hallon@example.com" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := homeWithRows(t, "hallon@example.com")
			m.homeFocus = homeFocusList
			c.setup(&m)

			if !m.inTextField() {
				t.Fatal("inTextField() is false inside the modal — h is swallowed as a shortcut")
			}
			out, _, done := m.globalKey("h")
			if done {
				t.Error("globalKey consumed h inside the modal")
			}
			if out.cur == screenHelp {
				t.Error("h opened Help from inside the modal; the address can never be typed")
			}
		})
	}
}

// Enter on a menu entry runMenuAction does not implement must be CONSUMED.
// Falling through ran the screen's own Enter handler instead, and on Home that
// handler creates an alias: the review measured Enter on "Delete" creating a
// real address through the API.
func TestUnwiredMenuEntryDoesNotFallThrough(t *testing.T) {
	m := homeWithRows(t, "alpha@example.com")
	m.width = 100
	m.naInput.SetValue("oops") // what the fall-through would have created

	// Every entry in the real tables is wired now (TestEveryMenuEntryActs
	// holds that line), so the guard is checked with an action no screen
	// claims — which is what a newly added button looks like before someone
	// implements it.
	out, cmd, done := m.runMenuAction("not-implemented-yet")
	if !done {
		t.Fatal("Enter on an unimplemented menu entry fell through to the screen")
	}
	if cmd != nil {
		t.Error("an unimplemented menu entry produced a command; it must do nothing")
	}
	if out.errMsg == "" {
		t.Error("an unimplemented entry must say so, not fail silently")
	}
	if out.naInput.Value() != "oops" {
		t.Error("the create field was consumed — the create path ran")
	}
}

// Every shortcut the generated Help lists must be handled by someone.
//
// The previous version of this test only asked globalKey, on a Home model,
// and listed everything else in an "owned" set — which is the same as not
// testing it: Config's ^r ^t ^x sat in the button table and in Help for three
// releases while updateOptions had no ctrl cases at all, and this test was
// green throughout. It now drives the REAL Update loop of the screen the key
// belongs to; see menus_parity_test.go.

// navigationRows, layout.go's doc comment and the CHANGELOG all say down
// enters the menu. It did not.
func TestDownEntersMenuFromLastRow(t *testing.T) {
	m := homeWithRows(t, "alpha@example.com")
	m.width = 100
	m.homeFocus = homeFocusList
	m.browseIdx = m.navCount() - 1

	if handled, _ := (&m).menuNav("down"); !handled || !m.menuActive {
		t.Error("down on the last row did not enter the bottom menu")
	}
	// From anywhere else it must still move between rows, not grab focus.
	m2 := homeWithRows(t, "alpha@example.com")
	m2.homeFocus = homeFocusNew
	if handled, _ := (&m2).menuNav("down"); handled {
		t.Error("down stole focus from a field instead of moving between rows")
	}
}

// Two halves, and both matter.
//
// 0.11.2 claimed "a Ctrl combination is never typed into a field". False:
// Ctrl keys are how a field is edited, and bubbles' textinput binds twelve of
// them. ^u in a password field has cleared the line for decades; here it
// opened an uninstall dialog.
//
// The fix is not to gate everything — it is to use keys the field does not
// own. Then a global shortcut can fire inside a field without stealing
// anything, and the user never has to Tab out first.
func TestGlobalShortcutWorksInsideAField(t *testing.T) {
	for _, focus := range []struct {
		name string
		f    int
	}{{"new alias field", homeFocusNew}, {"search field", homeFocusSearch}} {
		t.Run(focus.name, func(t *testing.T) {
			m := homeWithRows(t, "alpha@example.com")
			m.homeFocus = focus.f
			m.syncHomeFocus()
			if !m.inTextField() {
				t.Fatal("inTextField() is false while a field has focus")
			}
			got, _, done := m.globalKey("ctrl+o")
			if !done || got.cur != screenOptions {
				t.Error("ctrl+o is a free key and must work while a field has focus")
			}
		})
	}
}

// The other half: a key the FIELD owns must never be taken by a shortcut
// without a decision being written down. ^u is the clear-the-line reflex; it
// must reach the field untouched. ^h is the one deliberate exception (Help on
// every screen, including the login form) and is listed in fieldShortcuts,
// which TestFieldShortcutsAreRealCollisions checks is not stale.
func TestFieldKeysAreNotHijacked(t *testing.T) {
	if fieldShortcuts["ctrl+h"] == "" {
		t.Error("ctrl+h is taken from the field for Help; that has to stay documented in fieldShortcuts")
	}
	for _, k := range []string{"ctrl+u", "ctrl+e", "ctrl+a", "ctrl+w"} {
		m := homeWithRows(t, "alpha@example.com")
		m.homeFocus = homeFocusNew
		m.syncHomeFocus()
		if _, _, done := m.globalKey(k); done {
			t.Errorf("%s was consumed as a shortcut; the text field needs it for editing", k)
		}
	}
}

// No menu may bind the same key twice: shortcutRows merges ACROSS screens on
// purpose, so a duplicate inside one menu is invisible there.
func TestNoDuplicateKeyWithinAMenu(t *testing.T) {
	for _, sm := range allMenus() {
		seen := map[string]string{}
		for _, b := range sm.btns {
			if b.key == "" {
				continue
			}
			if prev, dup := seen[b.key]; dup {
				t.Errorf("%s binds %q to both %q and %q", sm.name, b.key, prev, b.label)
			}
			seen[b.key] = b.label
		}
	}
}

// Config screen vs config file, both ways (standard B6).
//
// It used to skip: the Theme group (14 keys) had no rows, and the four
// credential keys only appeared for the backend that was selected. The rows
// are generated from config.Fields now, so the screen cannot be missing a key
// and cannot show one the file does not have.
func TestConfigScreenMatchesConfigFile(t *testing.T) {
	cfg := config.Default()
	onScreen := map[string]bool{}
	for _, page := range optsPages() {
		for _, r := range optsRowsFor(cfg, page) {
			if r.key != "" {
				onScreen[r.key] = true
			}
		}
	}
	for _, f := range config.Fields {
		if !onScreen[f.Key] {
			t.Errorf("%s is in the config file but has no row on the Config screen", f.Key)
		}
	}
	for k := range onScreen {
		if !config.Known(k) {
			t.Errorf("the Config screen shows %q, which is not a config key", k)
		}
	}
}

// Every choice the Config screen offers must be a value the config file
// accepts, or picking it would write something Load() then reinterprets.
func TestConfigScreenChoicesAreValidValues(t *testing.T) {
	cfg := config.Default()
	for _, page := range optsPages() {
		for _, r := range optsRowsFor(cfg, page) {
			for _, c := range r.choices {
				if err := config.ValidateKey(r.key, c); err != nil {
					t.Errorf("%s offers %q, which the config file rejects: %v", r.key, c, err)
				}
			}
		}
	}
}

// menuIndexOf is the position of an action in the menu that is showing, so a
// test presses the entry rather than a number that moves when the table does.
func menuIndexOf(t *testing.T, btns []button, action string) int {
	t.Helper()
	for i, b := range btns {
		if b.action == action {
			return i
		}
	}
	t.Fatalf("no menu entry for %q", action)
	return -1
}

// Plus, Note and Delete run from the bottom menu open something that takes
// the keyboard, and the menu stayed active: globalKey then answered the next
// Enter with the same menu entry, which rebuilt the modal from scratch. The
// note that had just been typed was thrown away and notes.Set never ran, the
// delete confirmation lost the address and never dispatched, and Plus opened
// with menuSel still pointing at an entry of Home's menu, which on the Plus
// screen is "Back".
func TestAliasActionFromTheMenuReleasesTheMenu(t *testing.T) {
	type check func(t *testing.T, m model)
	cases := []struct {
		action string
		then   check
	}{
		{"note", func(t *testing.T, m model) {
			if !m.noteEdit {
				t.Fatal("the note editor did not open")
			}
			// Type a note and submit it, through the real loop.
			for _, r := range "bank" {
				mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				m = mm.(model)
			}
			mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = mm.(model)
			stored, err := notes.Load()
			if err != nil {
				t.Fatal(err)
			}
			if stored["hallon@example.com"] != "bank" {
				t.Errorf("stored notes = %v, want the typed note", stored)
			}
		}},
		{"delete", func(t *testing.T, m model) {
			if !m.confirmDelete {
				t.Fatal("the delete confirmation did not open")
			}
			for _, r := range "hallon@example.com" {
				mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				m = mm.(model)
			}
			if got := m.confirmInput.Value(); got != "hallon@example.com" {
				t.Fatalf("the confirmation field holds %q, want the typed address", got)
			}
			mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = mm.(model)
			if cmd == nil {
				t.Error("Enter on a complete confirmation dispatched nothing")
			}
		}},
		{"plus", func(t *testing.T, m model) {
			if m.cur != screenPlus {
				t.Fatalf("cur = %v, want the Plus screen", m.cur)
			}
			// menuSel came from Home's menu, where Plus is index 1. On the
			// Plus screen index 1 is Back, so Enter threw the form away
			// instead of submitting it.
			if m.menuSel != 0 {
				t.Errorf("menuSel = %d on a screen with its own menu, want 0", m.menuSel)
			}
			mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = mm.(model)
			// Enter on the form saves the + address, which is what Back
			// skipped: the screen closing afterwards is the normal flow.
			saved, err := plusalias.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(saved) != 1 {
				t.Errorf("saved + addresses = %v, want the one the form composed", saved)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.action, func(t *testing.T) {
			m := homeWithRows(t, "hallon@example.com")
			m.cur = screenHome
			m.homeFocus = homeFocusList
			m.listAll = true // the list is expanded, so a row is selectable
			m.browseIdx = 0
			m.menuActive = true
			m.menuSel = menuIndexOf(t, m.menuFor(), c.action)

			mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = mm.(model)

			if m.menuActive {
				t.Error("the menu is still active, so the next Enter goes to it and not to the screen")
			}
			c.then(t, m)
		})
	}
}

// Log out from the menu switched to the login screen with menuSel still at
// Home's "Log out" index, which is past the end of the login screen's shorter
// menu: it drew as active with nothing highlighted, the glitter loop kept
// ticking and the first Tab was swallowed deactivating an invisible
// selection.
func TestLogoutFromTheMenuLeavesTheLoginMenuAlone(t *testing.T) {
	m := homeWithRows(t, "hallon@example.com")
	m.cur = screenHome
	m.menuActive = true
	m.menuSel = menuIndexOf(t, m.menuFor(), "logout")

	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(model)

	if m.cur != screenLogin {
		t.Fatalf("cur = %v, want the login screen", m.cur)
	}
	if m.menuActive {
		t.Error("the menu is active on the login screen")
	}
	if m.menuSel >= len(m.menuFor()) {
		t.Errorf("menuSel = %d, past the end of a %d-entry menu", m.menuSel, len(m.menuFor()))
	}
}

// A taller window means less to scroll. The offsets were left where they
// were, and windowAt clamps the render, so the page looked right while the
// up-arrow did nothing for as many presses as the stale offset, and
// atLastRow() sent the down-arrow straight into the menu.
func TestScrollOffsetsAreClampedOnResize(t *testing.T) {
	m := model{cfg: config.Default(), cur: screenHelp, width: 80, height: 20}
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m = mm.(model)

	// Scroll to the end of the page at the short height.
	for i := 0; i < 200; i++ {
		mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = mm.(model)
	}
	if m.helpScroll == 0 {
		t.Fatal("Help did not scroll at height 20, so there is nothing to clamp")
	}
	m.diagScroll = m.helpScroll // the Doctor and Logs pages have the same shape

	mm, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 60})
	m = mm.(model)

	if m.helpScroll > m.helpMaxScroll() {
		t.Errorf("helpScroll = %d after the window grew, max is %d", m.helpScroll, m.helpMaxScroll())
	}
	if m.diagScroll > m.diagMaxScroll() {
		t.Errorf("diagScroll = %d after the window grew, max is %d", m.diagScroll, m.diagMaxScroll())
	}
	// And the page is at the top when there is nothing left to scroll.
	if m.helpMaxScroll() == 0 && m.helpScroll != 0 {
		t.Errorf("helpScroll = %d on a page that fits", m.helpScroll)
	}
}
