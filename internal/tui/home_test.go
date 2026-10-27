package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/services"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// "refreshing…" belongs to the alias fetch: domains/quota arriving first must
// not clear it, and without a session nothing is fetched so it isn't shown.
func TestHome_RefreshTextLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	t.Run("no session: refresh keys set no loading text", func(t *testing.T) {
		for _, k := range []tea.KeyMsg{{Type: tea.KeyCtrlR}, key("r")} {
			m := model{cfg: config.Default()}
			m.ensureHome()
			m.homeFocus = homeFocusList
			got, cmd := m.updateHome(k)
			if got.loading != "" || cmd != nil {
				t.Errorf("%s without session: loading=%q cmd=%v", k, got.loading, cmd != nil)
			}
		}
	})

	t.Run("domains and quota keep refreshing text; aliases clear it", func(t *testing.T) {
		m := model{cfg: config.Default(), sess: &session.Session{}, loading: "refreshing…"}
		got, _ := m.Update(domainsMsg{doms: []string{"example.com"}})
		m = got.(model)
		got, _ = m.Update(quotaMsg{q: &services.Quota{}})
		m = got.(model)
		if m.loading != "refreshing…" {
			t.Fatalf("loading cleared before aliases arrived: %q", m.loading)
		}
		got, _ = m.Update(aliasesMsg{m: map[string][]services.AddressEntry{}})
		if m = got.(model); m.loading != "" {
			t.Errorf("aliasesMsg should clear loading, got %q", m.loading)
		}
	})
}

// Emptying the search (Esc or deleting the query) re-arms the one-shot
// refresh so the next search fetches a fresh list again.
func TestHome_SearchRefreshRearms(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cases := []struct {
		name  string
		clear tea.KeyMsg
	}{
		{"esc", tea.KeyMsg{Type: tea.KeyEsc}},
		{"backspace to empty", tea.KeyMsg{Type: tea.KeyBackspace}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := model{cfg: config.Default(), sess: &session.Session{}}
			m.ensureHome()
			m.homeFocus = homeFocusSearch
			m.syncHomeFocus()
			m, _ = m.updateHome(key("t"))
			if !m.searchRefreshed {
				t.Fatal("first keystroke should refresh")
			}
			m.loading = ""
			m, _ = m.updateHome(c.clear)
			if m.searchRefreshed {
				t.Fatal("emptied search should re-arm the refresh")
			}
			m, cmd := m.updateHome(key("x"))
			if !m.searchRefreshed || cmd == nil || m.loading == "" {
				t.Errorf("next search should refresh again (refreshed=%v loading=%q)", m.searchRefreshed, m.loading)
			}
		})
	}
}

// The "showing X of Y · hidden by SHOW_ALIASES_FOR" line takes a row of the
// viewport, so one alias row fewer fits.
func TestViewHome_HiddenLineShrinksWindow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	render := func(filter string, extra int) (int, bool) {
		cfg := config.Default()
		cfg.ShowAliasesFor = filter
		m := model{cfg: cfg, sess: &session.Session{}, height: 30}
		m.ensureHome()
		m.listAll = true
		for i := 0; i < 40; i++ {
			m.browseRows = append(m.browseRows, browseRow{Domain: "example.com", Addr: fmt.Sprintf("a%02d@example.com", i), Enabled: true})
		}
		for i := 0; i < extra; i++ {
			m.browseRows = append(m.browseRows, browseRow{Domain: "x.com", Addr: fmt.Sprintf("b%02d@x.com", i), Enabled: true})
		}
		out := m.viewHome()
		return strings.Count(out, "@example.com"), strings.Contains(out, "hidden by SHOW_ALIASES_FOR")
	}
	plain, hasLine := render("", 0)
	if hasLine {
		t.Fatal("unfiltered list must not show the hidden line")
	}
	filtered, hasLine := render("example.com", 5)
	if !hasLine {
		t.Fatal("filtered list should show the hidden line")
	}
	if filtered != plain-1 {
		t.Errorf("rows with hidden line = %d, want %d (one fewer than %d)", filtered, plain-1, plain)
	}
}

// A lit bottom menu owns the keyboard. Home's list kept reading its own keys
// under it: j/k moved an invisible cursor, and "/" put the focus in the
// search field while the menu was still active, which is a state where
// inTextField() is false, so the next "q" typed into the field quit the
// program.
func TestHomeKeysDoNotReachTheListUnderALitMenu(t *testing.T) {
	m := homeWithRows(t, "a@example.com", "b@example.com")
	m.cur = screenHome
	m.listAll = true
	m.homeFocus = homeFocusList
	m.browseIdx = 0
	m.menuActive, m.menuSel = true, 0

	for _, key := range []string{"j", "k", "/"} {
		got, _ := press(t, m, key)
		if got.browseIdx != 0 {
			t.Errorf("%q moved the list cursor to %d under a lit menu", key, got.browseIdx)
		}
		if got.homeFocus != homeFocusList {
			t.Errorf("%q moved the focus to %d under a lit menu", key, got.homeFocus)
		}
	}

	// The state the bug led to: search focused with the menu lit. Typing in
	// the field must not be read as a shortcut.
	m.homeFocus = homeFocusSearch
	m.menuActive = false
	got, _ := press(t, m, "/")
	got, _ = press(t, got, "s")
	if got.menuActive {
		t.Fatal("typing in the search field activated the menu")
	}
	out, cmd := press(t, got, "q")
	if cmd != nil {
		t.Errorf("q typed into the search field returned a command (quit); searchInput=%q", out.searchInput.Value())
	}
}

// The list shrinks whenever the search or SHOW_ALIASES_FOR changes, and the
// only clamp compared against the unfiltered rows. With the cursor past the
// end no row was drawn, Enter matched nothing, and atLastRow() was true, so
// the next down-arrow jumped into the menu from an invisible cursor.
func TestHomeCursorIsClampedWhenTheListShrinks(t *testing.T) {
	m := homeWithRows(t, "a@example.com", "b@example.com", "c@example.com")
	m.cur = screenHome
	m.listAll = true
	m.homeFocus = homeFocusList
	m.browseIdx = m.navCount() - 1 // the Config row, the last selectable one

	// The filter narrows the list to one address, as a Config change would.
	m.cfg.ShowAliasesFor = "^a@example\\.com$"
	m.aliases = nil

	// Render first: Esc from Config returns without a key event.
	view := m.View()
	if !strings.Contains(view, "▶") {
		t.Errorf("no cursor is drawn after the list shrank:\n%s", view)
	}
	got, _ := press(t, m, "up")
	if got.browseIdx >= got.navCount() {
		t.Errorf("browseIdx = %d with navCount %d", got.browseIdx, got.navCount())
	}
	if _, cmd := press(t, got, "enter"); cmd == nil && got.browseIdx >= got.navCount() {
		t.Error("Enter does nothing because the cursor is past the end")
	}
}

// The three alias shortcuts are advertised by the bottom menu on every Home
// frame, and Enter on the same entries says "select an alias in the list
// first". The keys returned silently instead, so the user pressed a
// documented key that did nothing and got no hint why.
func TestHomeAliasShortcutsSayWhyTheyDidNothing(t *testing.T) {
	for _, key := range []string{"^s", "^t", "^x"} {
		t.Run(key, func(t *testing.T) {
			m := homeWithRows(t, "a@example.com")
			m.cur = screenHome
			m.listAll = true
			m.homeFocus = homeFocusSearch // a field has the focus

			got, _ := press(t, m, key)
			if got.errMsg == "" {
				t.Errorf("%s with a field focused said nothing", key)
			}

			// Same with the list focused but the cursor on the Config row.
			m2 := homeWithRows(t, "a@example.com")
			m2.cur = screenHome
			m2.listAll = true
			m2.homeFocus = homeFocusList
			m2.browseIdx = m2.navCount() - 1
			got2, _ := press(t, m2, key)
			if got2.errMsg == "" {
				t.Errorf("%s on the Config row said nothing", key)
			}
		})
	}
}
