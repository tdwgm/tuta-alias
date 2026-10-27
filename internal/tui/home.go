package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/tdwgm/tuta-alias/internal/login"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/notes"
)

// Home dashboard focus targets.
const (
	homeFocusNew = iota
	homeFocusSearch
	homeFocusList
)

// aliasActionHint is the always-on affordance shown to the right of the
// selected alias. It names the keys the list actually handles: the plain
// letters it used to advertise (+ n x) were never bound, so the three actions
// it points at did nothing for anyone who read the hint and believed it.
const aliasActionHint = "^s plus · ^t note · ^x delete"

// aliasRestoreHint replaces it for an alias that is already disabled, where
// ^x is the way back rather than another way down.
const aliasRestoreHint = "^s plus · ^t note · ^x re-enable"

func (m *model) ensureHome() {
	if m.homeReady {
		return
	}
	na := textinput.New()
	na.Placeholder = "new-alias  (or full addr@domain)"
	na.CharLimit = 64
	na.Width = 34
	na.Prompt = ""
	na.Focus()
	m.naInput = na

	se := textinput.New()
	se.Placeholder = "type to search…"
	se.CharLimit = 64
	se.Width = 34
	se.Prompt = ""
	m.searchInput = se

	if m.aliasNotes == nil {
		m.aliasNotes, _ = notes.Load()
		if m.aliasNotes == nil {
			m.aliasNotes = map[string]string{}
		}
	}

	m.homeFocus = homeFocusNew
	m.homeReady = true
}

// loadHomeCmd fetches the data the dashboard needs.
func (m model) loadHomeCmd() tea.Cmd {
	if m.sess == nil {
		return nil
	}
	cmds := []tea.Cmd{fetchAliases(m.sess), fetchDomains(m.sess)}
	if m.cfg.ShowQuota {
		cmds = append(cmds, fetchQuota(m.sess))
	}
	return tea.Batch(cmds...)
}

// listFilterRows applies the SHOW_ALIASES_FOR filter (a domain or a regexp;
// empty = all) used by the "List aliases" view.
func (m model) listFilterRows() []browseRow {
	pat := strings.TrimSpace(m.cfg.ShowAliasesFor)
	if pat == "" {
		return m.browseRows
	}
	re, err := regexp.Compile(pat)
	out := []browseRow{}
	for _, r := range m.browseRows {
		var match bool
		if err == nil {
			match = re.MatchString(r.Addr)
		} else {
			match = strings.Contains(r.Addr, pat)
		}
		if match {
			out = append(out, r)
		}
	}
	return out
}

// displayRows is what the list region currently shows: live search matches
// when there's a query, the SHOW_ALIASES_FOR-filtered set once "List aliases"
// is activated, otherwise nothing (the home starts list-free).
func (m model) displayRows() []browseRow {
	q := strings.ToLower(strings.TrimSpace(m.searchInput.Value()))
	if q != "" {
		out := []browseRow{}
		for _, r := range m.browseRows {
			if strings.Contains(strings.ToLower(r.Addr), q) {
				out = append(out, r)
			}
		}
		return out
	}
	if m.listAll {
		return m.listFilterRows()
	}
	return nil
}

// listPlaceholder is true when the list region shows the "List aliases"
// entry rather than rows (no search query and not yet expanded).
func (m model) listPlaceholder() bool {
	return strings.TrimSpace(m.searchInput.Value()) == "" && !m.listAll
}

// clampBrowseIdx keeps the list cursor inside the rows that are showing.
func (m *model) clampBrowseIdx() {
	if n := m.navCount(); m.browseIdx >= n {
		m.browseIdx = max(n-1, 0)
	}
	if m.browseIdx < 0 {
		m.browseIdx = 0
	}
}

// navCount is the number of selectable items in the list region. The two
// trailing items are always "Domains" and "Config"; in placeholder mode it's
// [List aliases, Domains, Config].
func (m model) navCount() int {
	if m.listPlaceholder() {
		return 3
	}
	return len(m.displayRows()) + 2
}

// domainsIdx is the browseIdx of the Domains item, just above Config.
func (m model) domainsIdx() int {
	return m.navCount() - 2
}

// configIdx is the browseIdx of the trailing Config item.
func (m model) configIdx() int {
	return m.navCount() - 1
}

func (m *model) syncHomeFocus() {
	m.naInput.Blur()
	m.searchInput.Blur()
	switch m.homeFocus {
	case homeFocusNew:
		m.naInput.Focus()
	case homeFocusSearch:
		m.searchInput.Focus()
	}
}

func (m model) updateHome(msg tea.Msg) (model, tea.Cmd) {
	m.ensureHome()
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	// The cursor can be left past the end: the list shrinks whenever the
	// search or SHOW_ALIASES_FOR changes, and the only clamp was aliasesMsg's,
	// which compares against the unfiltered rows. With browseIdx beyond
	// navCount no row is drawn, Enter matches nothing, and atLastRow() is
	// true, so the next down-arrow jumps into the menu from an invisible
	// cursor.
	(&m).clampBrowseIdx()
	// A lit menu owns the keyboard. Home's list kept reading j/k and "/"
	// under it, so the cursor moved with nothing highlighted and "/" put the
	// focus in the search field while the menu was still active: in that
	// state inTextField() is false, so typing "q" in the field quit the
	// program. Domains already gates its keys this way.
	if m.menuActive && !m.confirmDelete && !m.noteEdit {
		return m, nil
	}

	// Delete confirmation modal takes priority over everything else.
	if m.confirmDelete {
		switch k.String() {
		case "esc":
			m.confirmDelete = false
			m.deleteTarget = ""
			m.errMsg = ""
			return m, nil
		case "enter":
			if strings.TrimSpace(m.confirmInput.Value()) == m.deleteTarget {
				addr := m.deleteTarget
				m.confirmDelete = false
				m.deleteTarget = ""
				m.errMsg = ""
				m.loading = "deleting " + addr
				return m, opDelete(m.sess, addr)
			}
			m.errMsg = "address doesn't match — type it exactly, or Esc to cancel"
			return m, nil
		}
		var cmd tea.Cmd
		m.confirmInput, cmd = m.confirmInput.Update(msg)
		return m, cmd
	}

	// Note editor takes priority too.
	if m.noteEdit {
		switch k.String() {
		case "esc":
			m.noteEdit = false
			m.noteTarget = ""
			return m, nil
		case "enter":
			note := strings.TrimSpace(m.noteInput.Value())
			if err := notes.Set(m.noteTarget, note); err != nil {
				m.errMsg = "save note: " + err.Error()
			} else {
				if note == "" {
					delete(m.aliasNotes, m.noteTarget)
				} else {
					m.aliasNotes[m.noteTarget] = note
				}
				m.flash = "note saved for " + m.noteTarget
			}
			m.noteEdit = false
			m.noteTarget = ""
			return m, nil
		}
		var cmd tea.Cmd
		m.noteInput, cmd = m.noteInput.Update(msg)
		return m, cmd
	}

	// Global keys regardless of focus.
	switch k.String() {
	case "ctrl+l":
		return m.logoutHome()
	case "tab":
		m.homeFocus = (m.homeFocus + 1) % 3
		m.syncHomeFocus()
		return m, nil
	case "shift+tab":
		m.homeFocus = (m.homeFocus + 2) % 3
		m.syncHomeFocus()
		return m, nil
	case "ctrl+r":
		m.errMsg = ""
		return m.refreshHome()
	case "ctrl+y":
		return m.copySelected()
	case "ctrl+s", "ctrl+t", "ctrl+x":
		// Alias actions need a selected alias, so they only do something while
		// the list has focus — but they must be REACHED from anywhere, which
		// is the point of putting every shortcut on Ctrl. They sat inside the
		// list branch, so they did nothing whenever a field had focus.
		// Enter on the same three entries in the bottom menu answers "select
		// an alias in the list first". Returning silently left the user
		// pressing a key the menu advertises on every frame with no way to
		// tell why nothing happened.
		if m.homeFocus != homeFocusList {
			m.flash, m.errMsg = "", "select an alias in the list first"
			return m, nil
		}
		rows := m.displayRows()
		if m.listPlaceholder() || m.browseIdx >= len(rows) {
			m.flash, m.errMsg = "", "select an alias in the list first"
			return m, nil
		}
		sel := rows[m.browseIdx]
		switch k.String() {
		case "ctrl+s":
			return m.runAliasAction(sel, 0)
		case "ctrl+t":
			return m.runAliasAction(sel, 1)
		}
		return m.runAliasAction(sel, 2)
	}

	switch m.homeFocus {
	case homeFocusNew:
		switch k.String() {
		case "enter":
			return m.submitNewAlias()
		case "down":
			m.homeFocus = homeFocusSearch
			m.syncHomeFocus()
			return m, nil
		}
		var cmd tea.Cmd
		m.naInput, cmd = m.naInput.Update(msg)
		m.errMsg = ""
		return m, cmd

	case homeFocusSearch:
		switch k.String() {
		case "enter":
			// Enter from the query field copies the highlighted match, so a
			// search never has to be followed by a Tab into the list.
			return m.copySelected()
		case "down":
			m.homeFocus = homeFocusList
			m.syncHomeFocus()
			m.browseIdx = 0
			return m, nil
		case "up":
			m.homeFocus = homeFocusNew
			m.syncHomeFocus()
			return m, nil
		case "esc":
			m.searchInput.SetValue("")
			m.searchRefreshed = false
			m.browseIdx = 0
			return m, nil
		}
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		m.clampIdx()
		if strings.TrimSpace(m.searchInput.Value()) == "" {
			// Re-arm: the next search re-fetches a fresh list again.
			m.searchRefreshed = false
		}
		if !m.searchRefreshed && m.sess != nil && strings.TrimSpace(m.searchInput.Value()) != "" {
			m.searchRefreshed = true
			m.loading = "refreshing…"
			return m, tea.Batch(cmd, fetchAliases(m.sess))
		}
		return m, cmd

	default: // homeFocusList
		return m.updateHomeList(k)
	}
}

func (m *model) clampIdx() {
	max := m.navCount() - 1
	if m.browseIdx > max {
		m.browseIdx = max
	}
	if m.browseIdx < 0 {
		m.browseIdx = 0
	}
}

func (m model) updateHomeList(k tea.KeyMsg) (model, tea.Cmd) {
	rows := m.displayRows()
	placeholder := m.listPlaceholder()
	count := m.navCount()

	// Classify the current selection.
	onConfig := m.browseIdx == count-1
	onDomains := m.browseIdx == count-2
	onListAll := placeholder && m.browseIdx == 0
	var sel browseRow
	onAlias := false
	if !placeholder && m.browseIdx < len(rows) {
		sel = rows[m.browseIdx]
		onAlias = true
	}

	switch k.String() {
	case "up", "k":
		if m.browseIdx > 0 {
			m.browseIdx--
		} else {
			m.homeFocus = homeFocusSearch
			m.syncHomeFocus()
		}
	case "down", "j":
		if m.browseIdx < count-1 {
			m.browseIdx++
		}
	case "/":
		m.homeFocus = homeFocusSearch
		m.syncHomeFocus()
	case "enter":
		switch {
		case onListAll:
			m.listAll = true
			m.browseIdx = 0
		case onConfig:
			(&m).openConfig()
			return m, nil
		case onDomains:
			cmd := (&m).openDomains()
			return m, cmd
		case onAlias:
			return m.copyAddr(sel.Addr) // Enter == copy
		}
	case "c":
		// The reflex key, and safe here precisely because the LIST has focus:
		// a list is not a text field, so a plain letter collides with nothing.
		// ctrl+shift+c is not an option in this stack: bubbletea has no
		// ctrl+shift+<letter> at all, terminal multiplexers commonly run with
		// extended keys off, and terminals bind it to their own copy before
		// the app would ever see it.
		if onAlias {
			return m.copyAddr(sel.Addr)
		}
	}
	return m, nil
}

// logoutHome drops the session and returns to the login screen (^l and the
// Log out menu entry run exactly this).
func (m model) logoutHome() (model, tea.Cmd) {
	// A logout is two steps of very different length. Deleting the session file
	// is local, and has to be done before the login screen is drawn. Closing
	// the session on the server is a network call that can take a minute and a
	// half, so it is a command (see sessionclose.go). login.Logout does both,
	// the close first: on this loop that froze the screen, and as a command it
	// would leave the token on disk for as long as the close took.
	//
	// The old error goes first because renderBottom prints an error and hides
	// the flash, so a stale one would hide the line that says the close is
	// under way.
	m.errMsg = ""
	token, _, err := login.ClearSession()
	// A session file that survives the logout is the half that matters: the
	// access token is still on disk and the next start logs back in with it.
	// The error used to be discarded, so the screen said "logged out" either
	// way. It is the file's alone (login.ErrSessionFileRemains). The close is
	// sent whatever became of the file, because a token left on disk is then
	// a dead one, and its own failure is only a warning (see sessionClosed).
	if err != nil {
		m.errMsg = err.Error()
		logx.Errorf("logout: %v", err)
	}
	m.sess = nil
	m.flash = "logged out"
	m.cur = screenLogin
	// Run from the bottom menu, this left menuSel at Home's "Log out" index,
	// which is past the end of the login screen's shorter menu: the menu drew
	// as active with nothing highlighted, the glitter loop kept ticking, and
	// the first Tab was swallowed deactivating an invisible selection.
	m.menuActive, m.menuSel = false, 0
	m.loginState = newLoginState(m.cfg)
	closeCmd := (&m).beginClose(token)
	return m, tea.Batch(savedEmailCmd(), closeCmd)
}

// selectedAlias returns the alias row the list cursor points at.
func (m model) selectedAlias() (browseRow, bool) {
	if m.homeFocus != homeFocusList || m.listPlaceholder() {
		return browseRow{}, false
	}
	rows := m.displayRows()
	if m.browseIdx < 0 || m.browseIdx >= len(rows) {
		return browseRow{}, false
	}
	return rows[m.browseIdx], true
}

// refreshHome re-fetches the dashboard data. The "refreshing…" text is only
// shown when a fetch is actually dispatched; aliasesMsg clears it.
func (m model) refreshHome() (model, tea.Cmd) {
	cmd := m.loadHomeCmd()
	if cmd != nil {
		m.loading = "refreshing…"
	}
	return m, cmd
}

// copySelected copies the address the list cursor points at, from any focus.
// The cursor sits on a non-address item (the "List aliases" placeholder, the
// trailing Config row) or the search matched nothing → report it instead.
func (m model) copySelected() (model, tea.Cmd) {
	rows := m.displayRows()
	if m.browseIdx < 0 || m.browseIdx >= len(rows) {
		m.errMsg = "no matching address"
		return m, nil
	}
	return m.copyAddr(rows[m.browseIdx].Addr)
}

func (m model) copyAddr(addr string) (model, tea.Cmd) {
	if tool, err := copyToClipboard(addr); err == nil {
		m.flash = "copied " + addr + " (" + tool + ")"
	} else {
		m.errMsg = "clipboard: " + err.Error()
		logx.Errorf("clipboard: %v", err)
	}
	return m, nil
}

// runAliasAction performs action idx (0=plus, 1=note, 2=delete) on r.
func (m model) runAliasAction(r browseRow, idx int) (model, tea.Cmd) {
	// Whatever this opens takes the keyboard: a note editor, the delete
	// confirmation or the Plus screen. Leaving the bottom menu active meant
	// globalKey answered the next Enter with the same menu entry, so the text
	// that had just been typed was thrown away and the action never ran. Every
	// other screen switch (openConfig, openHelp, openDomains, "back") clears
	// the pair; these three did not.
	m.menuActive, m.menuSel = false, 0
	switch idx {
	case 0: // plus
		m.errMsg = ""
		m.plusState = newPlusState(r.Addr, m.cfg.PlusSuffixLenInt())
		m.cur = screenPlus
	case 1: // note (local, like plus-alias notes)
		ni := textinput.New()
		ni.CharLimit = 128
		ni.Width = 40
		ni.Prompt = ""
		ni.Placeholder = "where it's used…"
		ni.SetValue(m.aliasNotes[r.Addr])
		ni.Focus()
		m.noteInput = ni
		m.noteTarget = r.Addr
		m.noteEdit = true
		m.errMsg = ""
	case 2: // delete (typed confirmation), or re-enable a disabled alias
		if r.Primary {
			m.errMsg = "cannot delete primary address"
			return m, nil
		}
		if !r.Enabled {
			// Re-enabling is not destructive, so it does not ask twice.
			m.errMsg = ""
			m.loading = "re-enabling " + r.Addr
			return m, opRestore(m.sess, r.Addr)
		}
		ci := textinput.New()
		ci.Placeholder = r.Addr
		ci.CharLimit = 128
		ci.Width = 36
		ci.Prompt = ""
		ci.Focus()
		m.confirmInput = ci
		m.deleteTarget = r.Addr
		m.confirmDelete = true
		m.errMsg = ""
	}
	return m, nil
}

// submitNewAlias creates the alias typed in the New-alias field.
func (m model) submitNewAlias() (model, tea.Cmd) {
	v := strings.TrimSpace(m.naInput.Value())
	if v == "" {
		m.errMsg = "type an alias name first"
		return m, nil
	}
	var addr string
	if strings.Contains(v, "@") {
		addr = v
	} else {
		dom := m.cfg.DefaultDomain
		if dom == "" && len(m.domains) > 0 {
			dom = m.domains[0]
		}
		if dom == "" {
			m.errMsg = "no default domain — set one in Config"
			return m, nil
		}
		addr = v + "@" + dom
	}
	m.naInput.SetValue("")
	m.errMsg = ""
	m.loading = "creating " + addr
	return m, opCreate(m.sess, addr)
}

func (m model) viewHome() string {
	// Esc from Config comes back without a key event, so the first frame
	// after a narrowed SHOW_ALIASES_FOR renders before updateHome clamps.
	(&m).clampBrowseIdx()
	var b strings.Builder
	b.WriteString(m.banner())
	b.WriteString("\n")

	// Delete-confirmation modal replaces the dashboard body.
	if m.confirmDelete {
		b.WriteString("\n" + errStyle.Render("⚠ Delete (disable) alias") + "\n\n")
		b.WriteString("  " + txt("Target:  ") + keyStyle.Render(m.deleteTarget) + "\n")
		b.WriteString("  " + mutedStyle.Render("Type the full address to confirm:") + "\n")
		b.WriteString("  " + m.confirmInput.View() + "\n")
		if m.errMsg != "" {
			b.WriteString("\n" + errStyle.Render("✗ "+m.errMsg) + "\n")
		}
		b.WriteString("\n" + mutedStyle.Render("Enter to confirm  ·  Esc to cancel") + "\n")
		return b.String()
	}

	// Note editor replaces the dashboard body.
	if m.noteEdit {
		b.WriteString("\n" + titleStyle.Render("Note") + "\n\n")
		b.WriteString("  " + txt("Alias: ") + keyStyle.Render(m.noteTarget) + "\n")
		b.WriteString("  " + txt("Note:  ") + m.noteInput.View() + "\n")
		b.WriteString("\n" + mutedStyle.Render("Enter to save (empty clears)  ·  Esc to cancel") + "\n")
		return b.String()
	}
	if m.cfg.ShowQuota && m.quota != nil && uiLiveInfo != "full" {
		// LIVE_INFO=full already shows the quota under the banner.
		b.WriteString(mutedStyle.Render(fmt.Sprintf("quota: %d/%d used  ·  %d left",
			m.quota.UsedAliases, m.quota.TotalAliases, m.quota.Remaining())) + "\n")
	}
	b.WriteString("\n")

	naMark := "  "
	if m.homeFocus == homeFocusNew {
		naMark = iconStyle.Render("▶ ")
	}
	b.WriteString(naMark + txt("New alias: ") + m.naInput.View() + "  " + mutedStyle.Render("(Enter creates)") + "\n")

	seMark := "  "
	if m.homeFocus == homeFocusSearch {
		seMark = iconStyle.Render("▶ ")
	}
	b.WriteString(seMark + txt("Search:    ") + m.searchInput.View() + "\n\n")

	if m.loading != "" {
		b.WriteString(mutedStyle.Render(m.loading) + "\n")
	}

	b.WriteString(m.viewListRegion())

	// Status message + the one-line bottom menu. Both come from renderBottom
	// so every screen prints them identically; the old hand-written hint line
	// listed keys that Help now generates from the button table.
	b.WriteString("\n" + m.renderBottom() + "\n")
	return b.String()
}

// homeItemLine renders a non-alias selectable line (List aliases / Config).
func (m model) homeItemLine(label string, selected bool) string {
	mark := "  "
	if selected {
		mark = iconStyle.Render("▶ ")
	}
	return mark + txt(label) + "\n"
}

func (m model) domainsLabel() string {
	return fmt.Sprintf("☰ Domains %s", mutedStyle.Render(fmt.Sprintf("(%d)", len(m.domains))))
}

// viewListRegion renders the list area: either the placeholder items, or the
// alias rows, always followed by the trailing Config item.
func (m model) viewListRegion() string {
	focusList := m.homeFocus == homeFocusList
	var b strings.Builder

	if m.listPlaceholder() {
		b.WriteString(m.homeItemLine("List aliases "+mutedStyle.Render("(↓ then Enter — shows all)"), focusList && m.browseIdx == 0))
		b.WriteString(m.homeItemLine(m.domainsLabel(), focusList && m.browseIdx == 1))
		b.WriteString(m.homeItemLine("⚙ Config", focusList && m.browseIdx == 2))
		return b.String()
	}

	rows := m.displayRows()
	// Lines above the alias window that browseWindow must not count as rows.
	reserved := 7
	if !m.listPlaceholder() && strings.TrimSpace(m.searchInput.Value()) == "" {
		if hidden := len(m.browseRows) - len(rows); hidden > 0 {
			b.WriteString(mutedStyle.Render(fmt.Sprintf("showing %d of %d · %d hidden by SHOW_ALIASES_FOR=%q (search covers all)",
				len(rows), len(m.browseRows), hidden, strings.TrimSpace(m.cfg.ShowAliasesFor))) + "\n")
			reserved++
		}
	}
	if len(rows) == 0 {
		if m.loading == "" {
			b.WriteString(mutedStyle.Render("(no matching addresses)") + "\n")
		}
	} else {
		// Window around the selected alias (clamp so the Config row, which
		// sits at index len(rows), doesn't push the window out of range).
		winIdx := m.browseIdx
		if winIdx >= len(rows) {
			winIdx = len(rows) - 1
		}
		start, end := browseWindow(winIdx, len(rows), m.height-reserved)
		if start > 0 {
			b.WriteString(mutedStyle.Render(fmt.Sprintf("  ↑ %d more", start)) + "\n")
		}
		lastDom := ""
		for i := start; i < end; i++ {
			r := rows[i]
			if r.Domain != lastDom {
				b.WriteString(okStyle.Render(r.Domain) + "\n")
				lastDom = r.Domain
			}
			selected := i == m.browseIdx && focusList
			marker := "  "
			if selected {
				marker = iconStyle.Render("▶ ")
			} else if i == m.browseIdx {
				marker = mutedStyle.Render("· ")
			}
			tag := ""
			if r.Primary {
				tag = mutedStyle.Render(" (primary)")
			}
			if !r.Enabled {
				tag += mutedStyle.Render(" [disabled]")
			}
			if note := m.aliasNotes[r.Addr]; note != "" {
				tag += mutedStyle.Render("  — " + note)
			}
			line := marker + txt(r.Addr) + tag
			if selected {
				// Actions are shown automatically on the right of the selection.
				hint := aliasActionHint
				if !r.Enabled {
					hint = aliasRestoreHint
				}
				line += "   " + mutedStyle.Render(hint)
			}
			b.WriteString(line + "\n")
		}
		if end < len(rows) {
			b.WriteString(mutedStyle.Render(fmt.Sprintf("  ↓ %d more", len(rows)-end)) + "\n")
		}
	}

	// Trailing Domains and Config items.
	b.WriteString(m.homeItemLine(m.domainsLabel(), focusList && m.browseIdx == m.domainsIdx()))
	b.WriteString(m.homeItemLine("⚙ Config", focusList && m.browseIdx == m.configIdx()))
	return b.String()
}
