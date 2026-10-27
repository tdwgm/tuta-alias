package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// stubClipboard swaps the package-level clipboard helper for the duration of
// the test and records what was copied. The real clipboard is never touched.
func stubClipboard(t *testing.T, err error) *string {
	t.Helper()
	prev := copyToClipboard
	t.Cleanup(func() { copyToClipboard = prev })
	var got string
	copyToClipboard = func(s string) (string, error) {
		if err != nil {
			return "", err
		}
		got = s
		return "stub", nil
	}
	return &got
}

// homeWithRows builds a logged-in dashboard whose list region shows rows.
func homeWithRows(t *testing.T, addrs ...string) model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := model{cfg: config.Default(), sess: &session.Session{}}
	m.ensureHome()
	for _, a := range addrs {
		dom := a
		if i := strings.Index(a, "@"); i >= 0 {
			dom = a[i+1:]
		}
		m.browseRows = append(m.browseRows, browseRow{Domain: dom, Addr: a, Enabled: true})
	}
	return m
}

// A created alias is what the user is about to paste somewhere, so it is
// copied automatically.
func TestOpDone_CreateCopiesAddress(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	copied := stubClipboard(t, nil)

	m := model{cfg: config.Default(), sess: &session.Session{}}
	// The returned cmd (fetchAliases) is deliberately never executed — no
	// network call is made from this test.
	got, _ := m.Update(opDoneMsg{verb: verbCreated, addr: "spam@example.com"})
	mm := got.(model)

	if *copied != "spam@example.com" {
		t.Errorf("copied = %q, want %q", *copied, "spam@example.com")
	}
	if mm.flash != "created: spam@example.com (copied)" {
		t.Errorf("flash = %q, want %q", mm.flash, "created: spam@example.com (copied)")
	}
	if mm.errMsg != "" {
		t.Errorf("errMsg = %q, want empty", mm.errMsg)
	}
}

// The alias exists even when the clipboard helper is missing: keep the
// success flash and report the clipboard failure separately.
func TestOpDone_CreateKeepsFlashWhenCopyFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	stubClipboard(t, errors.New("no clipboard helper found"))

	m := model{cfg: config.Default(), sess: &session.Session{}}
	got, _ := m.Update(opDoneMsg{verb: verbCreated, addr: "spam@example.com"})
	mm := got.(model)

	if mm.flash != "created: spam@example.com" {
		t.Errorf("flash = %q, want the plain create flash", mm.flash)
	}
	if !strings.Contains(mm.errMsg, "clipboard") {
		t.Errorf("errMsg = %q, want the clipboard failure", mm.errMsg)
	}
}

// Delete must not copy anything.
func TestOpDone_DeleteDoesNotCopy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	copied := stubClipboard(t, nil)

	m := model{cfg: config.Default(), sess: &session.Session{}}
	got, _ := m.Update(opDoneMsg{verb: "deleted", addr: "spam@example.com"})
	mm := got.(model)

	if *copied != "" {
		t.Errorf("delete copied %q, want nothing", *copied)
	}
	if mm.flash != "deleted: spam@example.com" {
		t.Errorf("flash = %q, want %q", mm.flash, "deleted: spam@example.com")
	}
}

// Enter with the search field focused copies the highlighted match, so a
// search does not have to be followed by a Tab into the list.
func TestHome_EnterInSearchCopiesSelection(t *testing.T) {
	copied := stubClipboard(t, nil)

	m := homeWithRows(t, "alpha@example.com", "beta@example.com", "alphabet@example.com")
	m.homeFocus = homeFocusSearch
	m.syncHomeFocus()
	m.searchInput.SetValue("alpha")
	m.browseIdx = 1 // second match: alphabet@example.com

	got, _ := m.updateHome(tea.KeyMsg{Type: tea.KeyEnter})

	if *copied != "alphabet@example.com" {
		t.Errorf("copied = %q, want %q", *copied, "alphabet@example.com")
	}
	if !strings.Contains(got.flash, "copied alphabet@example.com") {
		t.Errorf("flash = %q, want a copy confirmation", got.flash)
	}
	if got.searchInput.Value() != "alpha" {
		t.Errorf("Enter must not alter the query, got %q", got.searchInput.Value())
	}
}

// ctrl+y copies the highlighted row from any focus, including the new-alias
// field where plain "c" is just a character.
func TestHome_CtrlYCopiesFromAnyFocus(t *testing.T) {
	for _, focus := range []int{homeFocusNew, homeFocusSearch, homeFocusList} {
		copied := stubClipboard(t, nil)

		m := homeWithRows(t, "alpha@example.com", "beta@example.com")
		m.listAll = true
		m.homeFocus = focus
		m.syncHomeFocus()
		m.browseIdx = 1

		got, _ := m.updateHome(tea.KeyMsg{Type: tea.KeyCtrlY})

		if *copied != "beta@example.com" {
			t.Errorf("focus %d: copied = %q, want %q", focus, *copied, "beta@example.com")
		}
		if got.errMsg != "" {
			t.Errorf("focus %d: errMsg = %q, want empty", focus, got.errMsg)
		}
	}
}

// "c" in the new-alias field is still typed text, never a copy.
func TestHome_CInNewAliasFieldTypes(t *testing.T) {
	copied := stubClipboard(t, nil)

	m := homeWithRows(t, "alpha@example.com")
	m.listAll = true
	m.homeFocus = homeFocusNew
	m.syncHomeFocus()

	got, _ := m.updateHome(key("c"))

	if *copied != "" {
		t.Errorf("typing copied %q, want nothing", *copied)
	}
	if got.naInput.Value() != "c" {
		t.Errorf("naInput = %q, want %q", got.naInput.Value(), "c")
	}
}

// Nothing highlighted (no matches, or the cursor on a non-address item) must
// say so rather than copy a stale address.
func TestHome_CopyWithoutSelectionReportsError(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*model)
		k     tea.KeyMsg
	}{
		{
			name: "enter in search, no matches",
			setup: func(m *model) {
				m.homeFocus = homeFocusSearch
				m.syncHomeFocus()
				m.searchInput.SetValue("zzz")
			},
			k: tea.KeyMsg{Type: tea.KeyEnter},
		},
		{
			name: "ctrl+y with a collapsed list",
			setup: func(m *model) {
				m.homeFocus = homeFocusNew
				m.syncHomeFocus()
			},
			k: tea.KeyMsg{Type: tea.KeyCtrlY},
		},
		{
			name: "ctrl+y on the trailing Config row",
			setup: func(m *model) {
				m.listAll = true
				m.homeFocus = homeFocusList
				m.syncHomeFocus()
				m.browseIdx = m.configIdx()
			},
			k: tea.KeyMsg{Type: tea.KeyCtrlY},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			copied := stubClipboard(t, nil)
			m := homeWithRows(t, "alpha@example.com")
			c.setup(&m)

			got, _ := m.updateHome(c.k)

			if *copied != "" {
				t.Errorf("copied %q, want nothing", *copied)
			}
			if got.errMsg != "no matching address" {
				t.Errorf("errMsg = %q, want %q", got.errMsg, "no matching address")
			}
		})
	}
}

// The footer must advertise ^Y, otherwise the global key is undiscoverable.
// Ctrl+Y (copy and keep the screen open) must stay discoverable. It used to
// live in a hand-written hint line; since the TUI Scaffold standard the bottom
// menu is generated from homeButtons, so the key has to be ON that entry or it
// disappears from both the menu and the generated Help.
func TestViewHome_MenuMentionsCtrlY(t *testing.T) {
	m := homeWithRows(t, "alpha@example.com")
	m.width = 100
	if out := m.viewHome(); !strings.Contains(out, "^y") {
		t.Errorf("bottom menu missing the ^y shortcut:\n%s", out)
	}
	var found bool
	for _, r := range shortcutRows(allMenus()) {
		if strings.Contains(r[0], "^y") {
			found = true
		}
	}
	if !found {
		t.Error("Help's shortcut list does not mention ^y")
	}
}
