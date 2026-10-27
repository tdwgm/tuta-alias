// menus.go — the bottom menu of every screen and the Help screen generated
// from it (TUI Scaffold standard v2.8, section B3/B4).
//
// The button tables here are the single source of truth for three things that
// used to be written three times and drift apart: the one-line menu at the
// bottom of each screen, the shortcut list in Help, and the keys the screens
// actually accept. Help is GENERATED from these tables — never hand-written —
// so it cannot claim a key the program does not have. The command list in Help
// is generated from cobra for the same reason: it was hand-written and had
// been missing eight of the seventeen commands.
//
// Every entry is also reachable with Enter from the menu itself: runMenuAction
// hands an action it does not own to the screen that does. Before that, ten of
// the twenty-one entries printed "not wired up yet" when you pressed Enter on
// them.
package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// shortenHome prints $HOME as "~". internal/config owns the implementation so
// the TUI, the checklist and the CLI shorten a path identically.
func shortenHome(p string) string { return config.ShortPath(p) }

func configPathForHelp() string  { return config.Path() }
func sessionPathForHelp() string { return session.Path() }

// cmdRoot is the cobra root, handed over by Run/RunInstallWizard so Help can
// list the real commands.
var cmdRoot *cobra.Command

// SetCommandRoot gives the TUI the cobra command tree Help lists.
func SetCommandRoot(c *cobra.Command) { cmdRoot = c }

// CommandRows returns one row per CLI command: name and its one-line Short,
// straight from cobra. Exported so a test can compare it with the command
// tree instead of with a list someone remembered to update.
func CommandRows(root *cobra.Command) [][2]string {
	if root == nil {
		return nil
	}
	var out [][2]string
	for _, c := range root.Commands() {
		if c.Hidden || !c.IsAvailableCommand() {
			continue
		}
		out = append(out, [2]string{c.Name(), c.Short})
	}
	return out
}

// menuTickMsg advances the glitter sweep across the selected menu entry. It is
// only scheduled while the menu has focus, so an idle screen costs nothing.
type menuTickMsg struct{}

const menuTickEvery = 90 * time.Millisecond

func menuTickCmd() tea.Cmd {
	return tea.Tick(menuTickEvery, func(time.Time) tea.Msg { return menuTickMsg{} })
}

// ^s and ^q are the classic XON/XOFF pair, and using them is safe here:
// bubbletea's raw mode clears IXON (charmbracelet/x/term term_unix.go:29), so
// the terminal passes both through to the application instead of treating them
// as flow control. Verified in the dependency, not assumed — if either ever
// looks dead while the app is otherwise responsive, that line is where to look.
//
// Button tables, one per screen. Labels are plain words, shortcuts lowercase.
// Help is ALWAYS last; q/esc are navigation and deliberately absent from the
// Help shortcut list (shortcutRows skips them) because they mean the same
// everywhere and are described in the navigation rows instead.

// homeButtons takes the state of the selected alias, because one entry
// changes meaning with it: on an alias that is already disabled, ^x is the
// way back (re-enable), not another way down.
func homeButtons(selectionDisabled bool) []button {
	del := button{label: "Delete", action: "delete", key: "^x"}
	if selectionDisabled {
		del = button{label: "Re-enable", action: "delete", key: "^x"}
	}
	return []button{
		{label: "Copy", action: "copy", key: "c/^y"}, // c is safe: the list is not a field
		{label: "Plus", action: "plus", key: "^s"},
		{label: "Note", action: "note", key: "^t"},
		del,
		{label: "Refresh", action: "refresh", key: "^r"},
		{label: "Config", action: "config", key: "^o"},
		{label: "Log out", action: "logout", key: "^l"},
		{label: "Quit", action: "quit", key: "^q"},
		{label: "Help", action: "help", key: "^h"},
	}
}

// loginButtons depends on whether there is a session, because that decides
// what Esc means here. Without one, Login IS the first screen and Esc quits;
// with one it was reached from Home and Esc goes back. The standard says q and
// Esc mean the same on every screen — back one level, quit on the first — so
// the menu has to say which of the two this is.
//
// The credential entries were missing entirely: the screen has handled ^b, ^k,
// ^r and ^s since 0.10, but with no button table to generate from, Help listed
// none of them. all=true lists every entry (Help); otherwise only the backends
// whose config is filled in, which is also what the screen itself shows.
func loginButtons(hasSession bool, cfg *config.Config, hasSaved, all bool) []button {
	var btns []button
	add := func(ok bool, label, action, key string) {
		if ok || all {
			btns = append(btns, button{label: label, action: action, key: key})
		}
	}
	add(backendConfigured(cfg, "bitwarden"), "Bitwarden", "fetch-bitwarden", "^b")
	add(backendConfigured(cfg, "keepassxc"), "KeePassXC", "fetch-keepassxc", "^k")
	add(backendConfigured(cfg, "bwrun"), "bw-run", "fetch-bwrun", "^r")
	// Saved login moved off ^g: globalKey takes ^g for Help before the login
	// screen ever sees a key, so ^g here was dead from the day Help got it.
	// ^l is free of bubbles' textinput keymap and unused on this screen.
	add(hasSaved, "Saved login", "saved-login", "^l")
	add(true, "Save login", "toggle-save", "^s")
	add(true, "Config", "config", "^o")
	if hasSession {
		add(true, "Back", "back", "esc")
	} else {
		add(true, "Quit", "quit", "^q")
	}
	add(true, "Help", "help", "^h")
	return btns
}

func configButtons() []button {
	return []button{
		{label: "Cancel", action: "cancel", key: "esc"},
		{label: "Reload", action: "reload", key: "^r"},
		{label: "Reset theme", action: "resettheme", key: "^t"},
		{label: "Uninstall", action: "uninstall", key: "^x"},
		{label: "Help", action: "help", key: "^h"},
	}
}

func plusButtons() []button {
	return []button{
		{label: "Regenerate", action: "regenerate", key: "^r"},
		{label: "Back", action: "back", key: "esc"},
		{label: "Help", action: "help", key: "^h"},
	}
}

func helpButtons() []button {
	return []button{
		{label: "Back", action: "back", key: "esc"},
	}
}

// menuFor returns the bottom menu of the screen currently shown.
func (m model) menuFor() []button {
	switch m.cur {
	case screenLogin:
		hasSaved := m.loginState != nil && m.loginState.savedEmail != ""
		return loginButtons(m.sess != nil, m.cfg, hasSaved, false)
	case screenOptions:
		return configButtons()
	case screenPlus:
		return plusButtons()
	case screenHelp:
		return helpButtons()
	case screenDoctor:
		return doctorButtons()
	case screenLogs:
		return logsButtons()
	case screenDomains:
		return domainsButtons()
	default:
		sel, ok := m.selectedAlias()
		return homeButtons(ok && !sel.Enabled)
	}
}

// allMenus is what Help merges over. Config's Ctrl keys work on the Config
// screen, so they belong in the list with their screen named.
func allMenus() []screenMenu {
	return []screenMenu{
		{name: "Home", btns: homeButtons(false)},
		{name: "Login", btns: loginButtons(false, nil, false, true)},
		{name: "Config", btns: configButtons()},
		{name: "Plus", btns: plusButtons()},
		{name: "Doctor", btns: doctorButtons()},
		{name: "Logs", btns: logsButtons()},
		{name: "Domains", btns: domainsButtons()},
	}
}

// navigationRows are the keys that are not menu entries: they work the same on
// every screen and would only add noise to the per-screen tables.
func navigationRows() [][2]string {
	return [][2]string{
		{"tab", "Move to the next field or row; from the last one, into the menu"},
		{"shift+tab", "Move back"},
		{"← / →", "Move between menu entries"},
		{"↑ / ↓", "Move between rows, and scroll Help and Config"},
		{"enter", "Act on the selected row or submit the field"},
		// Esc on the first screen does nothing on purpose: there is no level
		// to go back to and quitting on a stray Esc is not something to do to
		// someone. q is the one that quits, and this row has to say so.
		{"q / esc", "Back one level; on the first screen, q quits"},
		{"ctrl+c", "Quit from anywhere"},
	}
}

// backendList is config.BackendNames as prose, with the empty value left out
// (the sentence after it explains what empty means).
func backendList() string {
	names := make([]string, 0, len(config.BackendNames))
	for _, n := range config.BackendNames {
		if n != "" {
			names = append(names, n)
		}
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// bodyHelp renders the Help screen. Every shortcut it lists comes from the
// button tables above and every command from cobra, so Help and the program
// cannot contradict each other.
func bodyHelp() string {
	var b strings.Builder

	b.WriteString(sectionHeader("About") + "\n")
	b.WriteString(infoLine("Version", txt(Version)) + "\n")
	b.WriteString(infoLine("Config", txt(shortenHome(configPathForHelp()))) + "\n")
	b.WriteString(infoLine("Session", txt(shortenHome(sessionPathForHelp()))) + "\n\n")

	b.WriteString(sectionHeader("Navigation") + "\n")
	for _, r := range navigationRows() {
		b.WriteString(rowLine(false, r[0], txt(r[1])) + "\n")
	}
	b.WriteString("\n")

	b.WriteString(sectionHeader("Shortcuts") + "\n")
	for _, r := range shortcutRows(allMenus()) {
		b.WriteString(rowLine(false, r[0], txt(r[1])) + "\n")
	}
	b.WriteString("\n")

	if rows := CommandRows(cmdRoot); len(rows) > 0 {
		b.WriteString(sectionHeader("Commands") + "\n")
		for _, r := range rows {
			b.WriteString(rowLine(false, r[0], txt(r[1])) + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(sectionHeader("Aliases and + addresses") + "\n")
	b.WriteString(wrapText(termWidth, fgStyle,
		"An alias is a real address on one of the account's custom domains and is "+
			"created through Tuta's API. A + address (sub-address) is NOT: it routes "+
			"server-side on its own, so it is only stored locally and never sent to "+
			"Tuta. That is why deleting a + address frees no quota. The saved "+
			"ones are listed by `tuta-alias plus list` in the CLI; this screen "+
			"composes and copies them.") + "\n\n")

	b.WriteString(sectionHeader("Credential backends") + "\n")
	// The list comes from config.BackendNames, like the Config screen's
	// chooser and the install wizard's radio. Written out by hand it named
	// four of them and missed vault, which this branch added.
	b.WriteString(wrapText(termWidth, fgStyle,
		"CRED_BACKEND picks where the login comes from: "+backendList()+
			". Empty means type it every time. keyring is a login saved from "+
			"this form, vault is the built-in encrypted file. The TOTP code is "+
			"never stored.") + "\n")

	return b.String()
}

// helpLines is the Help body as lines, for scrolling.
func helpLines() []string {
	return strings.Split(strings.TrimRight(bodyHelp(), "\n"), "\n")
}

// helpRoom is how many body lines fit between the banner and the menu.
func (m model) helpRoom() int {
	if m.height <= 0 {
		return 0
	}
	// banner (9) + live block (2) + title (2) + rule and menu (3) + fine
	// print (3).
	room := m.height - 19
	if room < 5 {
		room = 5
	}
	return room
}

// helpMaxScroll is the largest useful scroll offset for the current size.
func (m model) helpMaxScroll() int {
	room := m.helpRoom()
	if room == 0 {
		return 0
	}
	return maxScroll(len(helpLines()), room)
}

// viewHelp renders the Help screen: banner, title, the generated body, and the
// menu. Esc/q returns to helpReturn — the screen it was opened from.
//
// The body is taller than most terminals, so it scrolls with ↑/↓ instead of
// running off the top: before this, everything below "Shortcuts" was simply
// unreachable on an 80x24 terminal.
func (m model) viewHelp() string {
	var b strings.Builder
	b.WriteString(m.banner() + "\n\n")
	b.WriteString(screenTitle("Help") + "\n\n")
	lines := helpLines()
	if room := m.helpRoom(); room > 0 && len(lines) > room {
		lines = windowAt(lines, m.helpScroll, room)
	}
	b.WriteString(strings.Join(lines, "\n") + "\n")
	b.WriteString("\n" + m.renderBottom())
	return b.String()
}

// renderBottom is the status message plus the one-line bottom menu, the last
// thing every screen prints. Keeping it in one place is what stops the screens
// drifting back into their own hint lines.
func (m model) renderBottom() string {
	var b strings.Builder
	if m.errMsg != "" {
		b.WriteString(flashLine(m.width, msgErr, m.errMsg) + "\n")
	} else if m.flash != "" {
		b.WriteString(flashLine(m.width, msgOK, m.flash) + "\n")
	}
	b.WriteString(footerRule() + "\n")
	b.WriteString(renderMenu(m.menuFor(), m.menuSel, m.menuActive, m.width))
	b.WriteString("\n\n" + renderLegal(m.width))
	return b.String()
}

// menuNav handles the keys that move in and out of the bottom menu. It returns
// true when the key was consumed, so a screen's own handler never sees it.
//
// Tab and ↑/↓ are deliberately asymmetric with ←/→: Tab toggles, the arrows
// only move inside the menu. That is the standard's shape (B3) and it keeps
// ←/→ free for text fields while a row has focus.
func (m *model) menuNav(key string) (bool, tea.Cmd) {
	switch key {
	case "tab":
		// Tab moves WITHIN the screen first and only enters the menu from the
		// last row. It used to toggle the menu unconditionally, which meant
		// it never reached a screen at all: Home could not move between its
		// fields, the login form could not Tab from email to password, and
		// the Plus screen's note field was unreachable by any key, because
		// Tab was the only thing that focused it. Measured 2026-10-26.
		if m.menuActive {
			m.menuActive = false
			return true, nil
		}
		if m.atLastRow() {
			m.menuActive, m.menuSel = true, 0
			return true, menuTickCmd()
		}
		return false, nil // the screen moves its own focus
	case "down":
		// Promised by navigationRows, layout.go's own doc comment and the
		// CHANGELOG, but never implemented. Only enters from the LAST row, so
		// it still moves between rows everywhere above that.
		if !m.menuActive && m.atLastRow() {
			m.menuActive, m.menuSel = true, 0
			return true, menuTickCmd()
		}
	case "up":
		if m.menuActive {
			m.menuActive = false
			return true, nil
		}
	case "left":
		if m.menuActive {
			m.menuSel = nextIdx(m.menuSel, -1, len(m.menuFor()))
			return true, nil
		}
	case "right":
		if m.menuActive {
			m.menuSel = nextIdx(m.menuSel, +1, len(m.menuFor()))
			return true, nil
		}
	}
	return false, nil
}

// openHelp switches to Help, remembering where to go back to.
func (m *model) openHelp() {
	if m.cur == screenHelp {
		return
	}
	m.helpReturn = m.cur
	m.cur = screenHelp
	m.helpScroll = 0
	m.menuActive, m.menuSel = false, 0
}

// inTextField reports whether a typed letter would land in an input rather
// than act as a shortcut. It is what makes "q goes back on every screen"
// safe: inside a field q is just a q.
func (m model) inTextField() bool {
	if m.menuActive {
		return false
	}
	switch m.cur {
	case screenHome:
		// The two modals open FROM homeFocusList, so focus alone does not say
		// whether a key is typed. Measured 2026-10-26: without these the
		// delete confirmation ate `h` and opened Help, which made every alias
		// containing an h impossible to delete from the TUI, because the
		// confirmation requires typing the address exactly.
		if m.confirmDelete || m.noteEdit {
			return true
		}
		return m.homeFocus == homeFocusNew || m.homeFocus == homeFocusSearch
	case screenLogin, screenPlus:
		return true
	case screenOptions:
		// Options only types while a row is being edited; otherwise the rows
		// are a list and letters are free.
		return m.optsState != nil && m.optsState.state == optsEditing
	}
	return false
}

// globalKey handles what every screen shares: menu navigation, Help, and the
// Config shortcut that the standard requires to work from anywhere. It
// returns done=true when the key was consumed.
func (m model) globalKey(key string) (model, tea.Cmd, bool) {
	// Help screen: Esc/q go back where Help was opened from; ↑/↓ scroll.
	if m.cur == screenHelp {
		switch key {
		case "esc", "q", "ctrl+h", "ctrl+g":
			m.cur = m.helpReturn
			m.menuActive, m.menuSel = false, 0
			return m, nil, true
		case "up", "k":
			if !m.menuActive && m.helpScroll > 0 {
				m.helpScroll--
				return m, nil, true
			}
		case "down", "j":
			if !m.menuActive && m.helpScroll < m.helpMaxScroll() {
				m.helpScroll++
				return m, nil, true
			}
		case "pgup":
			m.helpScroll = max(0, m.helpScroll-m.helpRoom())
			return m, nil, true
		case "pgdown", " ":
			m.helpScroll = min(m.helpMaxScroll(), m.helpScroll+m.helpRoom())
			return m, nil, true
		case "home":
			m.helpScroll = 0
			return m, nil, true
		case "end":
			m.helpScroll = m.helpMaxScroll()
			return m, nil, true
		}
		if handled, cmd := (&m).menuNav(key); handled {
			return m, cmd, true
		}
		if key == "enter" && m.menuActive {
			m.cur = m.helpReturn
			m.menuActive, m.menuSel = false, 0
			return m, nil, true
		}
		return m, nil, true // Help swallows the rest: nothing else to do here
	}

	// Doctor and Logs are read-only pages: Esc/q go back where they were
	// opened from, and up/down scroll them like Help.
	if m.cur == screenDoctor || m.cur == screenLogs {
		switch key {
		case "esc", "q":
			m.cur = m.subReturn
			m.menuActive, m.menuSel = false, 0
			return m, nil, true
		case "up", "k":
			if !m.menuActive && m.diagScroll > 0 {
				m.diagScroll--
				return m, nil, true
			}
		case "down", "j":
			if !m.menuActive && m.diagScroll < m.diagMaxScroll() {
				m.diagScroll++
				return m, nil, true
			}
		case "pgup":
			m.diagScroll = max(0, m.diagScroll-m.diagRoom())
			return m, nil, true
		case "pgdown", " ":
			m.diagScroll = min(m.diagMaxScroll(), m.diagScroll+m.diagRoom())
			return m, nil, true
		case "home":
			m.diagScroll = 0
			return m, nil, true
		case "end":
			m.diagScroll = m.diagMaxScroll()
			return m, nil, true
		}
	}

	// Domains has a cursor over its rows, so its navigation lives with it.
	if m.cur == screenDomains {
		if mm, done := m.domainsKey(key); done {
			return mm, nil, true
		}
	}

	if handled, cmd := (&m).menuNav(key); handled {
		return m, cmd, true
	}

	// Enter on a focused menu entry runs that entry.
	if m.menuActive && key == "enter" {
		btns := m.menuFor()
		if m.menuSel < len(btns) {
			return m.runMenuAction(btns[m.menuSel].action)
		}
	}

	// Only Help, Config and Quit are global. The standard asks for Config's
	// Ctrl keys everywhere, but that is not reachable here and the deviation
	// is measured, not assumed: bubbles v1.0.0's textinput binds twelve Ctrl
	// keys (a b d e f h k n p u v w) and the terminal owns c i j m z, leaving
	// nine free for more shortcuts than that. Config's ^r ^t ^x therefore stay
	// on the Config screen, where they cannot be mistaken for Home's ^t (Note)
	// — and the Config screen now actually handles them.
	//
	// NOT gated on inTextField(): ^g, ^o and ^q are verified free of bubbles'
	// textinput keymap (TestGlobalShortcutsAreFree reads it from the
	// dependency), so they steal nothing from a field and the user does not
	// have to Tab out first.
	switch key {
	case "ctrl+h", "ctrl+g":
		// ^h is the advertised Help key and is NOT gated on inTextField(), so
		// it works the same on every screen — including the Login form, which
		// is nothing but text fields, and Home's delete confirmation.
		//
		// The cost is measured, not assumed: bubbles' textinput binds ctrl+h
		// to DeleteCharacterBackward alongside backspace (textinput.go:78),
		// so inside a field ^h no longer deletes a character. The Backspace
		// key itself is a separate binding and still does. ^g stays as a
		// silent alias for anyone with it in their fingers; only one key is
		// advertised, because a menu entry listing two keys where one of them
		// only sometimes works is the half-truth this file exists to remove.
		(&m).openHelp()
		return m, nil, true
	case "ctrl+o":
		(&m).openConfig()
		return m, nil, true
	case "ctrl+q":
		return m, tea.Quit, true
	}

	// q and Esc: back one level, quit on the first screen. navigationRows has
	// promised this since the menu landed, but only Help implemented it, so
	// on every other screen q did nothing at all.
	if (key == "q" || key == "esc") && !m.inTextField() {
		switch m.cur {
		case screenOptions, screenPlus:
			if key == "q" {
				// Esc is the screen's own (it also cancels an edit and leaves
				// a sub-page), so only q is taken here. On a Config sub-page
				// q means the same as Esc: back one level, not out.
				if m.cur == screenOptions && m.optsState != nil && m.optsState.page != optsPageMain {
					(&m).setPage(optsPageMain)
					return m, nil, true
				}
				return m.runMenuAction("back")
			}
		case screenHome:
			if key == "q" && !m.confirmDelete && !m.noteEdit {
				return m, tea.Quit, true
			}
		}
	}
	return m, nil, false
}

// runMenuAction maps a menu entry to the same handler its shortcut uses, so a
// click-through from the menu and the shortcut can never diverge.
func (m model) runMenuAction(action string) (model, tea.Cmd, bool) {
	switch action {
	case "help":
		(&m).openHelp()
		return m, nil, true
	case "quit":
		return m, tea.Quit, true
	case "config":
		(&m).openConfig()
		return m, nil, true
	case "doctor":
		(&m).openDoctor()
		return m, nil, true
	case "logs":
		(&m).openLogs()
		return m, nil, true
	case "domains":
		cmd := (&m).openDomains()
		return m, cmd, true
	case "back", "cancel":
		switch m.cur {
		case screenDomains:
			m.cur = m.subReturn
			m.domainsState = nil
			m.menuActive, m.menuSel = false, 0
			return m, nil, true
		case screenDoctor, screenLogs:
			m.cur = m.subReturn
			m.menuActive, m.menuSel = false, 0
			return m, nil, true
		case screenPlus:
			m.plusState = nil
		case screenOptions:
			// Same return screen as Esc: Config is reachable from Home, from
			// Domains and from the login form.
			m.optsState = nil
			m.cur = m.optsReturn()
			m.menuActive, m.menuSel = false, 0
			return m, nil, true
		case screenLogin:
			if m.sess == nil {
				return m, tea.Quit, true
			}
			m.loginState = nil
		}
		m.cur = screenHome
		m.menuActive, m.menuSel = false, 0
		return m, nil, true
	}
	// Everything else belongs to a screen. An action no screen implements
	// must still be CONSUMED: falling through let the screen's own Enter
	// handler run instead, and on Home that handler creates an alias —
	// measured 2026-10-26, pressing Enter on the "Delete" entry created
	// oops@example.com through the real API.
	if mm, cmd, ok := m.runScreenAction(action); ok {
		return mm, cmd, true
	}
	m.flash = ""
	m.errMsg = "that menu entry is not wired up yet — use its shortcut"
	return m, nil, true
}

// runScreenAction runs a menu entry that belongs to the screen it is on. Each
// case calls exactly the handler the entry's shortcut calls, which is what
// makes the two paths impossible to drift apart.
func (m model) runScreenAction(action string) (model, tea.Cmd, bool) {
	switch m.cur {
	case screenHome:
		switch action {
		case "copy":
			mm, cmd := m.copySelected()
			return mm, cmd, true
		case "refresh":
			mm, cmd := m.refreshHome()
			return mm, cmd, true
		case "logout":
			mm, cmd := m.logoutHome()
			return mm, cmd, true
		case "plus", "note", "delete":
			sel, ok := m.selectedAlias()
			if !ok {
				m.flash = ""
				m.errMsg = "select an alias in the list first"
				return m, nil, true
			}
			idx := map[string]int{"plus": 0, "note": 1, "delete": 2}[action]
			mm, cmd := m.runAliasAction(sel, idx)
			return mm, cmd, true
		}
	case screenOptions:
		switch action {
		case "reload":
			mm, cmd := m.optsReload()
			return mm, cmd, true
		case "resettheme":
			mm, cmd := m.optsResetTheme()
			return mm, cmd, true
		case "uninstall":
			mm, cmd := m.optsAskUninstall()
			return mm, cmd, true
		}
	case screenPlus:
		if action == "regenerate" {
			mm, cmd := m.plusRegenerate()
			return mm, cmd, true
		}
	case screenDomains:
		switch action {
		case "domains-check":
			mm, cmd := m.domainsCheck()
			return mm, cmd, true
		case "domains-txt":
			mm, cmd := m.domainsCopyTXT()
			return mm, cmd, true
		case "domains-refresh":
			mm, cmd := m.domainsRefresh()
			return mm, cmd, true
		}
	case screenDoctor, screenLogs:
		switch action {
		case "diag-refresh":
			mm, cmd := m.diagRefresh()
			return mm, cmd, true
		case "logs-clear":
			mm, cmd := m.logsClear()
			return mm, cmd, true
		}
	case screenLogin:
		switch action {
		case "fetch-bitwarden", "fetch-keepassxc", "fetch-bwrun":
			name := strings.TrimPrefix(action, "fetch-")
			if !backendConfigured(m.cfg, name) {
				return m, nil, false
			}
			mm, cmd := m.startBackendFetch(name)
			return mm, cmd, true
		case "saved-login":
			mm, cmd := m.startSavedLogin()
			return mm, cmd, true
		case "toggle-save":
			if m.loginState != nil {
				m.loginState.save = !m.loginState.save
			}
			return m, nil, true
		}
	}
	return m, nil, false
}

// atLastRow reports whether the content cursor is on its last row, which is
// when a further "down" should fall into the bottom menu rather than do
// nothing. Screens that are a form rather than a list never auto-enter: there
// Tab is the way in, so a stray arrow cannot pull focus out of a field.
func (m model) atLastRow() bool {
	switch m.cur {
	case screenHome:
		if m.confirmDelete || m.noteEdit {
			return false
		}
		return m.homeFocus == homeFocusList && m.browseIdx >= m.navCount()-1
	case screenOptions:
		return m.optsState != nil && m.optsState.state != optsEditing &&
			m.optsIdx >= len(m.optsState.rows)-1
	case screenLogin:
		// The submit button is the last row of the form.
		return m.loginState != nil && m.loginState.focus == 3
	case screenPlus:
		return m.plusState != nil && m.plusState.focus == 1
	case screenHelp:
		// Only once the body has been scrolled to the end, so ↓ reads the
		// page first and enters the menu after.
		return m.helpScroll >= m.helpMaxScroll()
	case screenDoctor, screenLogs:
		return m.diagScroll >= m.diagMaxScroll()
	case screenDomains:
		d := m.domainsState
		return d == nil || (d.cursor >= len(d.list)-1 && d.scroll >= m.domainsMaxScroll())
	}
	return false
}

// gatedShortcuts are keys that only fire OUTSIDE a text field. Nothing is
// gated today: a gated key on the Login form (all text fields) would never
// fire at all, which is worse than a documented collision. The map and
// TestGatedShortcutsYieldToTheField stay so that gating one later keeps its
// guard.
var gatedShortcuts = map[string]bool{}

// fieldShortcuts are the keys taken from the text field ON PURPOSE, with what
// each one costs written down. TestNoShortcutCollidesWithTextInput allows
// exactly these and fails for any new collision, so taking a field key is a
// decision someone has to make here rather than something that can happen by
// accident.
var fieldShortcuts = map[string]string{
	"ctrl+h": "Help, on every screen (costs the field's ^h backspace; the Backspace key still works)",
	"ctrl+b": "Login: fetch from Bitwarden (costs the field's cursor-left)",
	"ctrl+k": "Login: fetch from KeePassXC; Domains: check DNS (costs the field's kill-to-end)",
	"ctrl+v": "Domains: copy the ownership TXT (costs the field's paste shortcut, but the screen has no field)",
}
