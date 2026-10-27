package tui

import (
	"fmt"
	"slices"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	keyringlib "github.com/zalando/go-keyring"

	"github.com/tdwgm/tuta-alias/internal/creds"

	"strings"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/notes"
	"github.com/tdwgm/tuta-alias/internal/plusalias"
	"github.com/tdwgm/tuta-alias/internal/services"
	"github.com/tdwgm/tuta-alias/internal/session"
)

func key(s string) tea.KeyMsg {
	// Every shortcut is a Ctrl combination since 0.11.2, so the helper has to
	// build those too — a rune message would never reach the handlers.
	if t, ok := ctrlKeys[s]; ok {
		return tea.KeyMsg{Type: t}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

var ctrlKeys = map[string]tea.KeyType{
	"ctrl+s": tea.KeyCtrlS, "ctrl+t": tea.KeyCtrlT, "ctrl+x": tea.KeyCtrlX, "ctrl+g": tea.KeyCtrlG,
	"ctrl+o": tea.KeyCtrlO, "ctrl+q": tea.KeyCtrlQ, "ctrl+y": tea.KeyCtrlY,
	"ctrl+r": tea.KeyCtrlR, "ctrl+l": tea.KeyCtrlL, "ctrl+h": tea.KeyCtrlH,
}

// Bug regression: aliasesMsg must populate browseRows (was never built).
func TestAliasesMsg_PopulatesBrowseRows(t *testing.T) {
	m := model{cfg: config.Default(), sess: &session.Session{}}
	grouped := map[string][]services.AddressEntry{
		"example.com": {{MailAddress: "spam@example.com", Enabled: true}},
		"x.com":       {{MailAddress: "a@x.com", Enabled: true, Primary: true}},
	}
	got, _ := m.Update(aliasesMsg{m: grouped})
	mm := got.(model)
	if len(mm.browseRows) != 2 {
		t.Fatalf("browseRows = %d, want 2", len(mm.browseRows))
	}
}

// The list viewport must keep the selected row visible on long lists.
func TestBrowseWindow_KeepsIndexVisible(t *testing.T) {
	if s, e := browseWindow(0, 40, 0); s != 0 || e != 40 {
		t.Errorf("height 0 → (%d,%d), want (0,40)", s, e)
	}
	if s, e := browseWindow(5, 10, 50); s != 0 || e != 10 {
		t.Errorf("fits → (%d,%d), want (0,10)", s, e)
	}
	total, height := 40, 24 // visible = 16
	for _, idx := range []int{0, 20, 39} {
		s, e := browseWindow(idx, total, height)
		if idx < s || idx >= e {
			t.Errorf("idx %d not in window [%d,%d)", idx, s, e)
		}
		if e-s != 16 {
			t.Errorf("window size = %d, want 16", e-s)
		}
	}
}

// Dashboard: New-alias field uses the default domain for a bare name.
func TestSubmitNewAlias_DefaultDomain(t *testing.T) {
	m := model{cfg: &config.Config{DefaultDomain: "example.com"}, sess: &session.Session{}}
	m.naInput = textinput.New()
	m.naInput.SetValue("shop")
	got, _ := m.submitNewAlias()
	if got.loading != "creating shop@example.com" {
		t.Fatalf("loading = %q, want creating shop@example.com", got.loading)
	}
	if got.naInput.Value() != "" {
		t.Errorf("naInput should be cleared, got %q", got.naInput.Value())
	}
}

// Dashboard: a full addr@domain is used verbatim.
func TestSubmitNewAlias_FullAddress(t *testing.T) {
	m := model{cfg: &config.Config{DefaultDomain: "example.com"}, sess: &session.Session{}}
	m.naInput = textinput.New()
	m.naInput.SetValue("custom@example.org")
	got, _ := m.submitNewAlias()
	if got.loading != "creating custom@example.org" {
		t.Fatalf("loading = %q", got.loading)
	}
}

// Dashboard: bare name with no default domain is an error, not a request.
func TestSubmitNewAlias_NoDomain(t *testing.T) {
	m := model{cfg: &config.Config{}, sess: &session.Session{}}
	m.naInput = textinput.New()
	m.naInput.SetValue("shop")
	got, _ := m.submitNewAlias()
	if got.errMsg == "" {
		t.Fatalf("expected errMsg when no default domain")
	}
	if got.loading != "" {
		t.Errorf("should not start a request, loading=%q", got.loading)
	}
}

// Dashboard: with no query and not expanded, the list is empty (search-only
// home) and the "List aliases" placeholder is shown.
func TestDisplayRows_DefaultEmpty(t *testing.T) {
	m := model{cfg: config.Default()}
	m.browseRows = []browseRow{{Addr: "spam@example.com"}, {Addr: "a@x.com"}}
	m.searchInput = textinput.New()
	if rows := m.displayRows(); len(rows) != 0 {
		t.Fatalf("default displayRows should be empty, got %+v", rows)
	}
	if !m.listPlaceholder() {
		t.Error("expected the List-aliases placeholder by default")
	}
}

// Dashboard: a search query filters by substring.
func TestDisplayRows_Search(t *testing.T) {
	m := model{cfg: config.Default()}
	m.browseRows = []browseRow{
		{Addr: "spam@example.com"}, {Addr: "shodan@example.com"}, {Addr: "toni@example.org"},
	}
	m.searchInput = textinput.New()
	m.searchInput.SetValue("sho")
	rows := m.displayRows()
	if len(rows) != 1 || rows[0].Addr != "shodan@example.com" {
		t.Fatalf("search displayRows = %+v", rows)
	}
}

// "List aliases" (listAll) honours SHOW_ALIASES_FOR as a domain or regexp.
func TestListFilterRows(t *testing.T) {
	rows := []browseRow{
		{Addr: "spam@example.com"}, {Addr: "shop@example.com"}, {Addr: "toni@example.org"},
	}
	m := model{cfg: &config.Config{ShowAliasesFor: "example.com"}, listAll: true}
	m.browseRows = rows
	m.searchInput = textinput.New()
	if got := m.displayRows(); len(got) != 2 {
		t.Errorf("domain filter: got %d, want 2", len(got))
	}
	m.cfg.ShowAliasesFor = "^sho"
	if got := m.displayRows(); len(got) != 1 || got[0].Addr != "shop@example.com" {
		t.Errorf("regexp filter: got %+v", got)
	}
	m.cfg.ShowAliasesFor = ""
	if got := m.displayRows(); len(got) != 3 {
		t.Errorf("empty filter should show all, got %d", len(got))
	}
}

// Dashboard: '+' on the list opens the Plus screen seeded from the selection.
func TestDashboardPlusKey_OpensPlus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := model{
		cfg:        config.Default(),
		cur:        screenHome,
		homeReady:  true,
		homeFocus:  homeFocusList,
		listAll:    true,
		browseRows: []browseRow{{Domain: "example.com", Addr: "spam+old@example.com"}},
	}
	got, _ := m.updateHome(key("ctrl+s"))
	if got.cur != screenPlus {
		t.Fatalf("cur = %v, want screenPlus", got.cur)
	}
	if got.plusState == nil || got.plusState.local != "spam" || got.plusState.domain != "example.com" {
		t.Fatalf("plusState not seeded: %+v", got.plusState)
	}
}

// Enter on the Plus screen saves the composed address and returns Home.
func TestPlusEnter_SavesAndReturnsHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := model{cfg: config.Default(), cur: screenPlus}
	m.plusState = newPlusState("spam@example.com", 5)
	m.plusState.suffix.SetValue("github")

	got, _ := m.updatePlus(tea.KeyMsg{Type: tea.KeyEnter})
	if got.cur != screenHome {
		t.Fatalf("cur = %v, want screenHome", got.cur)
	}
	entries, err := plusalias.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 1 || entries[0].Address != "spam+github@example.com" {
		t.Fatalf("saved entries = %+v", entries)
	}
}

// Invalid suffix blocks save and surfaces an error.
func TestPlusEnter_RejectsInvalidSuffix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := model{cfg: config.Default(), cur: screenPlus}
	m.plusState = newPlusState("spam@example.com", 5)
	m.plusState.suffix.SetValue("")

	got, _ := m.updatePlus(tea.KeyMsg{Type: tea.KeyEnter})
	if got.cur != screenPlus || got.errMsg == "" {
		t.Fatalf("expected to stay on Plus with an error; cur=%v err=%q", got.cur, got.errMsg)
	}
	if entries, _ := plusalias.Load(); len(entries) != 0 {
		t.Fatalf("nothing should be saved, got %+v", entries)
	}
}

// autoLoginBackend only fires for a configured, TUI-usable backend.
func TestAutoLoginBackend_NilCases(t *testing.T) {
	// No backend configured.
	if b := autoLoginBackend(&config.Config{}); b != nil {
		t.Error("no backend should yield nil")
	}
	// Backend selected but its required item isn't set.
	if b := autoLoginBackend(&config.Config{CredBackend: "bitwarden"}); b != nil {
		t.Error("bitwarden without BW_ITEM should yield nil")
	}
	// bwrun is excluded (it uses syscall.Exec; unusable inside the TUI).
	if b := autoLoginBackend(&config.Config{CredBackend: "bwrun", BWRunItem: "tuta"}); b != nil {
		t.Error("bwrun must be excluded from auto-login")
	}
}

// Delete from the dashboard must require typing the exact address.
func TestDelete_RequiresTypedConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mk := func() model {
		m := model{
			cfg:        config.Default(),
			cur:        screenHome,
			sess:       &session.Session{},
			homeReady:  true,
			homeFocus:  homeFocusList,
			listAll:    true,
			browseRows: []browseRow{{Domain: "example.com", Addr: "spam@example.com", Enabled: true}},
		}
		return m
	}

	// 'x' opens the confirm modal, does NOT delete yet.
	m, cmd := mk().updateHome(key("ctrl+x"))
	if !m.confirmDelete || m.deleteTarget != "spam@example.com" {
		t.Fatalf("expected confirm modal for spam@example.com, got confirm=%v target=%q", m.confirmDelete, m.deleteTarget)
	}
	if cmd != nil {
		t.Error("'x' must not dispatch a delete before confirmation")
	}
	// The modal must render with the target address.
	if out := m.viewHome(); !strings.Contains(out, "spam@example.com") || !strings.Contains(strings.ToLower(out), "confirm") {
		t.Errorf("confirm modal render missing target/confirm text:\n%s", out)
	}

	// Wrong text → stays in confirm, no delete.
	m.confirmInput.SetValue("wrong@example.com")
	m2, cmd := m.updateHome(tea.KeyMsg{Type: tea.KeyEnter})
	if !m2.confirmDelete || cmd != nil {
		t.Errorf("mismatched address must not delete; confirm=%v", m2.confirmDelete)
	}
	if m2.errMsg == "" {
		t.Error("expected a mismatch error")
	}

	// Exact text → dispatches the delete and closes the modal.
	m2.confirmInput.SetValue("spam@example.com")
	m3, cmd := m2.updateHome(tea.KeyMsg{Type: tea.KeyEnter})
	if m3.confirmDelete {
		t.Error("exact match should close the modal")
	}
	if cmd == nil {
		t.Error("exact match should dispatch the delete command")
	}

	// Esc cancels without deleting.
	mc, cmd := m.updateHome(tea.KeyMsg{Type: tea.KeyEsc})
	if mc.confirmDelete || cmd != nil {
		t.Error("Esc should cancel the confirmation")
	}
}

// Primary addresses can't be deleted (no confirm modal).
func TestDelete_PrimaryRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := model{
		cfg:        config.Default(),
		cur:        screenHome,
		sess:       &session.Session{},
		homeReady:  true,
		homeFocus:  homeFocusList,
		listAll:    true,
		browseRows: []browseRow{{Domain: "example.com", Addr: "toni@example.com", Primary: true}},
	}
	got, _ := m.updateHome(key("ctrl+x"))
	if got.confirmDelete {
		t.Error("primary address must not open the delete confirm")
	}
	if got.errMsg == "" {
		t.Error("expected an error for primary delete attempt")
	}
}

// The note action opens an editor and saves the note to the local store.
func TestNoteAction_SavesNote(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := model{
		cfg:        config.Default(),
		cur:        screenHome,
		sess:       &session.Session{},
		homeReady:  true,
		homeFocus:  homeFocusList,
		listAll:    true,
		aliasNotes: map[string]string{},
		browseRows: []browseRow{{Domain: "example.com", Addr: "spam@example.com"}},
	}
	// 'n' opens the note editor for the selected alias.
	m, _ = m.updateHome(key("ctrl+t"))
	if !m.noteEdit || m.noteTarget != "spam@example.com" {
		t.Fatalf("expected note editor for spam@example.com, got edit=%v target=%q", m.noteEdit, m.noteTarget)
	}
	m.noteInput.SetValue("github signup")
	m, _ = m.updateHome(tea.KeyMsg{Type: tea.KeyEnter})
	if m.noteEdit {
		t.Error("note editor should close after save")
	}
	if m.aliasNotes["spam@example.com"] != "github signup" {
		t.Errorf("in-memory note not set: %+v", m.aliasNotes)
	}
	saved, _ := notes.Load()
	if saved["spam@example.com"] != "github signup" {
		t.Errorf("note not persisted: %+v", saved)
	}
}

// The banner renders and embeds the app version.
func TestRenderBanner_HasVersion(t *testing.T) {
	b := renderBanner()
	if b == "" {
		t.Fatal("banner is empty")
	}
	if !strings.Contains(b, consts.AppVersion()) {
		t.Errorf("banner missing version %q", consts.AppVersion())
	}
}

// viewHome must render a populated, scrolled dashboard without panicking and
// keep the selected row + scroll indicators visible (the integrated render
// path the redesign hinges on).
func TestViewHome_PopulatedRenders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := model{cfg: config.Default(), sess: &session.Session{MailAddress: "user@tuta.com"}, height: 24}
	m.ensureHome()
	m.homeFocus = homeFocusList
	m.listAll = true // expand the full list so rows render
	// ~37 rows across 3 domains.
	for i := 0; i < 12; i++ {
		m.browseRows = append(m.browseRows, browseRow{Domain: "example.com", Addr: fmt.Sprintf("a%02d@example.com", i)})
	}
	for i := 0; i < 13; i++ {
		m.browseRows = append(m.browseRows, browseRow{Domain: "example.org", Addr: fmt.Sprintf("b%02d@example.org", i)})
	}
	for i := 0; i < 12; i++ {
		m.browseRows = append(m.browseRows, browseRow{Domain: "tuta.com", Addr: fmt.Sprintf("c%02d@tuta.com", i)})
	}
	m.browseIdx = 30

	out := m.viewHome()
	if !strings.Contains(out, m.browseRows[30].Addr) {
		t.Errorf("selected row %q not visible in render", m.browseRows[30].Addr)
	}
	if !strings.Contains(out, "more") {
		t.Errorf("expected a scroll indicator for a 37-row list at height 24")
	}
	if !strings.Contains(out, "user@tuta.com") {
		t.Errorf("status line missing account")
	}
}

// An expired-session API error clears the session and routes to login.
func TestRouteIfExpired_ClearsAndRoutes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := model{cfg: config.Default(), sess: &session.Session{}, cur: screenHome}
	got, _, done := m.routeIfExpired(&api.APIError{Status: 440})
	if !done {
		t.Fatal("expected routeIfExpired to handle HTTP 440")
	}
	if got.sess != nil {
		t.Errorf("session should be cleared")
	}
	if got.cur != screenLogin {
		t.Errorf("cur = %v, want screenLogin", got.cur)
	}
	// A non-expiry error must NOT route.
	m2 := model{cfg: config.Default(), sess: &session.Session{}, cur: screenHome}
	if _, _, done := m2.routeIfExpired(&api.APIError{Status: 500}); done {
		t.Error("HTTP 500 must not be treated as expiry")
	}
}

// Ctrl+R refreshes from any focus (text inputs would swallow a plain "r").
func TestHome_CtrlRRefreshesFromSearch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := model{cfg: config.Default(), sess: &session.Session{}}
	m.ensureHome()
	m.homeFocus = homeFocusSearch
	m.syncHomeFocus()
	m2, cmd := m.updateHome(tea.KeyMsg{Type: tea.KeyCtrlR})
	if cmd == nil || m2.loading == "" {
		t.Fatalf("ctrl+r should dispatch a reload (cmd=%v loading=%q)", cmd, m2.loading)
	}
}

// The first search keystroke re-fetches aliases once; later ones don't.
func TestHome_FirstSearchRefreshesOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := model{cfg: config.Default(), sess: &session.Session{}}
	m.ensureHome()
	m.homeFocus = homeFocusSearch
	m.syncHomeFocus()
	m, _ = m.updateHome(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if !m.searchRefreshed || m.loading == "" {
		t.Fatalf("first search keystroke should refresh (refreshed=%v loading=%q)", m.searchRefreshed, m.loading)
	}
	m.loading = ""
	m, _ = m.updateHome(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if m.loading != "" {
		t.Errorf("second keystroke must not refresh again (loading=%q)", m.loading)
	}
}

// The expanded list says how many addresses SHOW_ALIASES_FOR hides.
func TestViewHome_ShowsHiddenByFilterCount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cfg := config.Default()
	cfg.ShowAliasesFor = "example.com"
	m := model{cfg: cfg, sess: &session.Session{}, height: 40}
	m.ensureHome()
	m.listAll = true
	m.browseRows = []browseRow{
		{Domain: "example.com", Addr: "a@example.com", Enabled: true},
		{Domain: "example.org", Addr: "b@example.org", Enabled: true},
		{Domain: "tuta.io", Addr: "c@tuta.io", Enabled: true},
	}
	out := m.viewHome()
	if !strings.Contains(out, "showing 1 of 3") || !strings.Contains(out, "2 hidden") {
		t.Errorf("missing hidden-by-filter line in:\n%s", out)
	}
}

// Ctrl+S ticks "Save login"; a successful login then stores email+password
// (never the TOTP) in the keyring.
func TestLogin_SaveCheckboxStoresLoginOnSuccess(t *testing.T) {
	keyringlib.MockInit()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := model{cfg: config.Default(), cur: screenLogin}
	m.loginState = newLoginState(m.cfg)
	m.loginState.fields[0].SetValue("u@tuta.com")
	m.loginState.fields[1].SetValue("secret")
	m.loginState.fields[2].SetValue("123456")
	m, _ = m.updateLogin(tea.KeyMsg{Type: tea.KeyCtrlS})
	if !m.loginState.save {
		t.Fatal("ctrl+s should tick Save login")
	}
	m, _ = m.updateLogin(loginDoneMsg{sess: &session.Session{MailAddress: "u@tuta.com"}})
	c, err := creds.LoadLogin()
	if err != nil || c.Email != "u@tuta.com" || c.Password != "secret" || c.TOTP != "" {
		t.Fatalf("saved login = %+v, %v", c, err)
	}
	if !strings.Contains(m.flash, "saved") {
		t.Errorf("flash should confirm the save: %q", m.flash)
	}
}

// The saved-login shortcut loads the keyring login and waits on the TOTP
// field instead of submitting.
//
// This test used to press ^g and call updateLogin directly. Both were wrong
// in the same way: Update runs globalKey FIRST, and globalKey has taken ^g for
// Help since the Help screen landed — so the shortcut the login screen
// advertised had been dead for every real user while the test stayed green.
// It is ^l now, and the keys go through the real Update loop.
func TestLogin_SavedLoginShortcutFillsFormAndFocusesTOTP(t *testing.T) {
	keyringlib.MockInit()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := creds.SaveLogin("u@tuta.com", "secret"); err != nil {
		t.Fatal(err)
	}
	m := model{cfg: config.Default(), cur: screenLogin}
	m.loginState = newLoginState(m.cfg)
	// The saved address arrives asynchronously (keyring lookup off the UI loop).
	got, _ := m.Update(savedEmailCmd()())
	m = got.(model)
	if m.loginState.savedEmail != "u@tuta.com" {
		t.Fatalf("savedEmail = %q", m.loginState.savedEmail)
	}

	// ^l through the whole loop: reads the keyring off the UI loop, then fills.
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	m = got.(model)
	if cmd == nil {
		t.Fatal("ctrl+l should dispatch the keyring read as a command")
	}
	got, _ = m.Update(cmd())
	m = got.(model)
	s := m.loginState
	if s.phase != loginPhasePicker {
		t.Fatal("the saved login must not submit — the TOTP is still missing")
	}
	if s.fields[0].Value() != "u@tuta.com" || s.fields[1].Value() != "secret" || s.focus != 2 {
		t.Errorf("form not filled/focused: email=%q focus=%d", s.fields[0].Value(), s.focus)
	}
	if !strings.Contains(m.viewLogin(), "Ctrl+L: saved login (u@tuta.com)") {
		t.Error("login view should offer the saved login")
	}
}

// ^g and ^h open Help from the login screen, and must not be mistaken for a
// login shortcut again.
func TestLogin_HelpKeysOpenHelp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, k := range []tea.KeyType{tea.KeyCtrlH, tea.KeyCtrlG} {
		m := model{cfg: config.Default(), cur: screenLogin, width: 100, height: 40}
		m.loginState = newLoginState(m.cfg)
		got, _ := m.Update(tea.KeyMsg{Type: k})
		if got.(model).cur != screenHelp {
			t.Errorf("%v did not open Help from the login screen", k)
		}
	}
}

// ^x on an alias that is ALREADY disabled re-enables it, and does not ask for
// a typed confirmation: re-enabling is not destructive. The TUI could disable
// an alias and show it as "[disabled]" forever, with no way back except the
// CLI's `delete --restore`.
func TestDisabledAliasIsReEnabledWithoutConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := model{
		cfg:        config.Default(),
		cur:        screenHome,
		sess:       &session.Session{},
		homeReady:  true,
		homeFocus:  homeFocusList,
		listAll:    true,
		browseRows: []browseRow{{Domain: "example.com", Addr: "old@example.com", Enabled: false}},
	}
	out, cmd := m.updateHome(key("ctrl+x"))
	if out.confirmDelete {
		t.Error("re-enabling asked for a typed confirmation")
	}
	if cmd == nil {
		t.Fatal("^x on a disabled alias dispatched nothing")
	}
	if !strings.Contains(out.loading, "re-enabling") {
		t.Errorf("loading = %q, want it to say what is happening", out.loading)
	}
	// And the menu entry says so, instead of offering to delete it again.
	m.browseIdx = 0
	var labels []string
	for _, b := range m.menuFor() {
		labels = append(labels, b.label)
	}
	if !slices.Contains(labels, "Re-enable") {
		t.Errorf("the Home menu offers %v on a disabled alias, with no Re-enable", labels)
	}
}
