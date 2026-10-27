// options.go — the Config screen.
//
// Every row is generated from config.Fields, which is what makes the screen
// and the file 1:1 by construction: a key added to the field table gets a row
// here with no further work, and a key cannot be edited here without existing
// in the file. The screen used to hold its own hand-written list of rows, and
// the Theme group (14 keys) was simply missing from it, while the menu
// advertised a "Reset theme" that nothing implemented.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/creds"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/serve"
	"github.com/tdwgm/tuta-alias/internal/session"
	"github.com/tdwgm/tuta-alias/internal/update"
	"github.com/tdwgm/tuta-alias/internal/vault"
)

// optsRow is one renderable line on the Options screen.
type optsRow struct {
	kind   string // "header" | "field" | "info" | "action" | "blank"
	label  string
	value  string
	action string // see activateOptsRow
	key    string // config key when kind=="field"
	// choices is the fixed choice list for a "choice" row, straight from
	// config.Choices(key) so the screen can never offer a value the file
	// would reject.
	choices []string
}

func (r optsRow) selectable() bool {
	return r.kind == "field" || r.kind == "action"
}

// optsLabels gives the keys a human label. A key missing from here still gets
// a row (prettified from its name), so forgetting an entry costs a nicer
// label, never the row itself.
var optsLabels = map[string]string{
	"DEFAULT_DOMAIN":   "Default domain",
	"TUI_ENABLED":      "TUI as default",
	"PLUS_SUFFIX_LEN":  "Plus suffix length",
	"SHOW_QUOTA":       "Show quota on Home",
	"SHOW_ALIASES_FOR": "Show aliases for",
	"CRED_BACKEND":     "Backend",
	"BW_ITEM":          "BW item",
	"BW_RUN_ITEM":      "BW-run item",
	"KEEPASS_DB":       "KeePass DB",
	"KEEPASS_ENTRY":    "KeePass entry",
	"CACHE_TTL":        "Cache TTL",
	"TUTA_PROXY":       "Tuta proxy",
	"SERVE_HOST":       "Host",
	"SERVE_PORT":       "Port",
	"COMPLETION_BASH":  "Bash",
	"COMPLETION_ZSH":   "Zsh",
	"COMPLETION_FISH":  "Fish",
	"INSTALL_PATH":     "Binary path",
	"LOG_LEVEL":        "Level",
	"LOG_FILE":         "File",
	"THEME":            "Accent",
	"COLOR_BANNER":     "Banner",
	"COLOR_HEADER":     "Headers",
	"COLOR_TEXT":       "Text",
	"COLOR_MUTED":      "Muted text",
	"COLOR_SHORTCUT":   "Shortcuts",
	"COLOR_SELECTED":   "Selected text",
	"COLOR_RULE":       "Rule",
	"COLOR_SUBTITLE":   "Under logo",
	"COLOR_LIVE":       "Live info text",
	"BANNER_TEXT":      "Banner text",
	"BANNER_FONT":      "Banner font",
	"LIVE_INFO":        "Live info",
	"MENU_EFFECT":      "Menu effect",
}

func optsLabel(key string) string {
	if l, ok := optsLabels[key]; ok {
		return l
	}
	return strings.ToUpper(key[:1]) + strings.ToLower(strings.ReplaceAll(key[1:], "_", " "))
}

// boolKeys are rendered as [x]/[ ] and toggled by Enter rather than opening a
// chooser for two values.
func isBoolKey(key string) bool {
	c := config.Choices(key)
	return len(c) == 2 && c[0] == "true" && c[1] == "false"
}

// optsPage is which page of the Config screen is showing. The Theme group is
// 14 of the 32 keys and nobody opens Config to change a colour, so it lives
// behind a "Theme" row that opens its own list rather than making the main
// page three screens long.
type optsPage int

const (
	optsPageMain optsPage = iota
	optsPageTheme
)

// optsPages is every page, for the tests that check no key is unreachable.
func optsPages() []optsPage { return []optsPage{optsPageMain, optsPageTheme} }

// optsRowsFor assembles one page of the Config screen from config.Fields.
func optsRowsFor(cfg *config.Config, page optsPage) []optsRow {
	values := config.ToMap(cfg)
	if page == optsPageTheme {
		rows := []optsRow{
			{kind: "info", label: "Theme:", value: mutedStyle.Render("changes apply as you move through the list")},
			{kind: "blank"},
		}
		for _, f := range config.Fields {
			if f.Section != config.ThemeSection() {
				continue
			}
			rows = append(rows, fieldRow(f.Key, values[f.Key], cfg))
		}
		return append(rows,
			optsRow{kind: "blank"},
			optsRow{kind: "header", label: strings.Repeat("─", 60)},
			optsRow{kind: "action", label: "Reset theme to defaults", action: "resettheme"},
			optsRow{kind: "action", label: "Back to Config", action: "back-page"},
		)
	}

	rows := []optsRow{
		{kind: "info", label: "Config:", value: shortenHome(config.Path())},
		{kind: "info", label: "Session:", value: sessionStatusLine()},
		{kind: "blank"},
	}
	section := ""
	for _, f := range config.Fields {
		if f.Section == config.ThemeSection() {
			continue // its own page
		}
		if f.Section != section {
			if section != "" {
				rows = append(rows, optsRow{kind: "blank"})
			}
			rows = append(rows, optsRow{kind: "header", label: f.Section})
			section = f.Section
		}
		rows = append(rows, fieldRow(f.Key, values[f.Key], cfg))
		// Rows that are not config keys but belong with the key above them.
		switch f.Key {
		case "CACHE_TTL":
			rows = append(rows, optsRow{
				kind: "field", label: "Cache status", value: cacheStatusLine(cfg), action: "clear-cache",
			})
		case "UPDATE_INTERVAL":
			rows = append(rows,
				optsRow{kind: "field", label: "Status", value: updateStatusLine(), action: "update-check"},
				optsRow{kind: "action", label: "Update now", action: "update-now"},
			)
		case "CRED_BACKEND":
			if cfg.CredBackend == "vault" {
				rows = append(rows, optsRow{
					kind: "field", label: "Vault", value: vaultStatusLine(), action: "vault-info",
				})
			}
		case "SERVE_PORT":
			rows = append(rows,
				optsRow{kind: "field", label: "API token", value: maskedToken(), action: "copy-token"},
				optsRow{kind: "action", label: "Rotate API token", action: "rotate-token"},
			)
		}
	}
	rows = append(rows,
		optsRow{kind: "blank"},
		optsRow{kind: "header", label: "More"},
		optsRow{kind: "field", label: "Theme", value: themeSummary(cfg), action: "open-theme"},
		optsRow{kind: "field", label: "Doctor", value: mutedStyle.Render("check install, config, session and tooling") + bracketStyle.Render("  ›"), action: "doctor"},
		optsRow{kind: "field", label: "Logs", value: logsSummary(cfg), action: "logs"},
		optsRow{kind: "blank"},
		optsRow{kind: "header", label: strings.Repeat("─", 60)},
		optsRow{kind: "action", label: "Edit config in $EDITOR", action: "edit-config"},
		optsRow{kind: "action", label: "Reload config from disk", action: "reload"},
		optsRow{kind: "action", label: "Uninstall tuta-alias", action: "uninstall"},
	)
	return rows
}

// buildOptsRows is the main page.
func buildOptsRows(cfg *config.Config) []optsRow { return optsRowsFor(cfg, optsPageMain) }

// themeSummary is what the Theme row shows without opening it.
func themeSummary(cfg *config.Config) string {
	return txt(strings.Join([]string{cfg.ColorAccent, cfg.BannerFont, cfg.MenuEffect}, " · ")) +
		bracketStyle.Render("  ›")
}

// logsSummary says whether anything is being written, and how much. It does
// not call logx.Setup: a render must not reconfigure the logger.
func logsSummary(cfg *config.Config) string {
	if cfg.LogLevel == "off" {
		return txt("off") + bracketStyle.Render("  ›")
	}
	if size := logx.SizeOf(logx.PathFor(cfg.LogFile)); size >= 0 {
		return txt(fmt.Sprintf("%s · %d B", cfg.LogLevel, size)) + bracketStyle.Render("  ›")
	}
	return txt(cfg.LogLevel) + mutedStyle.Render(" · empty") + bracketStyle.Render("  ›")
}

// fieldRow builds the row for one config key: a toggle for a bool, a chooser
// when the key has a fixed choice list, free text otherwise.
func fieldRow(key, value string, cfg *config.Config) optsRow {
	r := optsRow{kind: "field", label: optsLabel(key), key: key, value: orDash(value)}
	switch {
	case isBoolKey(key):
		r.action, r.value = "toggle", yesno(value == "true")
	case key == "DEFAULT_DOMAIN":
		// A chooser filled from the account's own domains, with free text as
		// the fallback when there is no session to ask.
		r.action = "change-domain"
	case config.Choices(key) != nil:
		r.action, r.choices = "choice", config.Choices(key)
	default:
		r.action = "edit"
	}
	if key == "INSTALL_PATH" && strings.TrimSpace(value) == "" {
		r.value = mutedStyle.Render(shortenHome(config.DefaultInstallPath()) + " (default)")
	}
	return r
}

// updateStatusLine shows the last check, without making a new one: the Config
// screen must not reach the network while it renders.
func updateStatusLine() string {
	st, ok := update.Cached()
	switch {
	case !ok:
		return mutedStyle.Render("not checked yet (Enter checks now)")
	case st.Err != "":
		return warnStyle.Render(st.Err)
	case st.Newer:
		return warnStyle.Render(st.Ref + " points at a different build")
	default:
		return okStyle.Render("this is the build " + st.Ref + " points at")
	}
}

// vaultStatusLine says whether the encrypted vault exists, without opening it.
func vaultStatusLine() string {
	if !vault.Exists() {
		return mutedStyle.Render("not created — run `tuta-alias vault set`")
	}
	return txt(shortenHome(vault.Path()))
}

func orDash(s string) string {
	if s == "" {
		return mutedStyle.Render("(not set)")
	}
	return s
}

func yesno(b bool) string {
	if b {
		return "[x]"
	}
	return "[ ]"
}

func sessionStatusLine() string {
	p := shortenHome(session.Path())
	s, err := session.Load()
	if err != nil {
		return p + mutedStyle.Render("  (no session)")
	}
	return p + okStyle.Render(fmt.Sprintf("  (active: %s)", s.MailAddress))
}

// maskedToken shows whether a token exists, without creating one. Reading it
// through serve.GetOrCreateToken wrote ~/.cache/tuta-alias/api-token as a
// side effect of merely opening the Config screen.
func maskedToken() string {
	tok, err := serve.PeekToken()
	if err != nil || tok == "" {
		return mutedStyle.Render("(not created yet)")
	}
	return strings.Repeat("*", min(len(tok), 8))
}

func cacheStatusLine(cfg *config.Config) string {
	if cfg.CredBackend == "" {
		return mutedStyle.Render("(no backend)")
	}
	key := cacheKeyFor(cfg)
	if key == "" {
		return mutedStyle.Render("(no cache key)")
	}
	ttl := cfg.CacheTTL
	if cfg.CredBackend == "keyring" {
		// The saved login is always stored without expiry (creds.SaveLogin);
		// CACHE_TTL only applies to the Bitwarden/KeePass caches.
		ttl = "forever"
	}
	st := creds.Status(key, ttl)
	if !st.Exists {
		return mutedStyle.Render("(empty)")
	}
	switch st.Mode {
	case "reboot":
		return okStyle.Render("valid, clears on reboot")
	case "forever":
		return okStyle.Render("valid, no expiry")
	default:
		remain := time.Until(st.ExpiresAt).Truncate(time.Minute)
		if remain <= 0 {
			return mutedStyle.Render("(expired)")
		}
		return okStyle.Render(fmt.Sprintf("valid, %s left", remain))
	}
}

// cacheKeyFor returns the keyring user key for the active backend.
func cacheKeyFor(cfg *config.Config) string {
	switch cfg.CredBackend {
	case "bitwarden":
		return "bw-session"
	case "keyring":
		return creds.SavedLoginCacheKey
	case "vault":
		return creds.VaultCacheKey
	case "keepassxc":
		if cfg.KeePassDB == "" {
			return ""
		}
		return creds.KeePassCacheKey(cfg.KeePassDB)
	}
	return ""
}

// --- Options screen state machine ---

type optsEditState int

const (
	optsBrowsing            optsEditState = iota
	optsEditing                           // inline textinput on selected field
	optsChoosing                          // inline radio list (choices or domains)
	optsConfirmingUninstall               // y/Y/n prompt
)

type optsState struct {
	rows     []optsRow
	page     optsPage
	state    optsEditState
	input    textinput.Model
	radio    []string
	radioIdx int
}

// setPage switches page and rebuilds the rows, putting the cursor on the
// first thing that can be selected.
func (m *model) setPage(page optsPage) {
	m.optsState.page = page
	m.optsState.rows = optsRowsFor(m.cfg, page)
	m.optsState.state = optsBrowsing
	m.optsIdx = nextSelectable(m.optsState.rows, -1, +1)
}

func newOptsState(cfg *config.Config) *optsState {
	return &optsState{rows: buildOptsRows(cfg), state: optsBrowsing}
}

// openConfig switches to the Config screen AND builds its state.
//
// Every path that only set m.cur = screenOptions (^o, the Config menu entry,
// the Home list row) left optsState nil, and View then printed a bare
// "loading..." with no banner and no rows until the next keystroke happened
// to reach updateOptions. It is one helper now, and viewOptions renders from
// a freshly built state rather than that placeholder if anything still
// manages to get there.
func (m *model) openConfig() {
	if m.optsState == nil {
		m.optsState = newOptsState(m.cfg)
		m.optsIdx = nextSelectable(m.optsState.rows, -1, +1)
	}
	// Where Esc goes back to, like Domains, Doctor and Logs. It used to be
	// Home unconditionally: Config opened over the Domains screen came back
	// to Home, and opened over the login form it set cur to Home while the
	// view still drew the login form, so the frame carried Home's bottom menu
	// (Copy, Delete, Log out) under a form with no session behind it.
	if m.cur != screenOptions {
		m.subReturn = m.cur
	}
	m.cur = screenOptions
	m.menuActive, m.menuSel = false, 0
}

// optsReturn is the screen Esc goes back to from Config. Home is the fallback
// for a Config opened before anything recorded a return screen.
func (m model) optsReturn() screen {
	if m.subReturn == screenOptions {
		return screenHome
	}
	return m.subReturn
}

// updateOptions handles all key events on the Options screen.
func (m model) updateOptions(msg tea.Msg) (model, tea.Cmd) {
	if m.optsState == nil {
		m.optsState = newOptsState(m.cfg)
	}
	s := m.optsState

	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return m, nil
	}

	switch s.state {
	case optsEditing:
		if k.String() == "esc" {
			s.state = optsBrowsing
			return m, nil
		}
		if k.String() == "enter" {
			m2, _ := m.saveOptsValue(s.rows[m.optsIdx].key, s.input.Value())
			return m2, nil
		}
		s.input, _ = s.input.Update(msg)
		return m, nil

	case optsChoosing:
		switch k.String() {
		case "esc":
			s.state = optsBrowsing
		case "j", "down":
			if s.radioIdx < len(s.radio)-1 {
				s.radioIdx++
			}
		case "k", "up":
			if s.radioIdx > 0 {
				s.radioIdx--
			}
		case "enter":
			m2, _ := m.saveOptsValue(s.rows[m.optsIdx].key, s.radio[s.radioIdx])
			return m2, nil
		}
		return m, nil

	case optsConfirmingUninstall:
		switch k.String() {
		case "esc", "n", "N":
			s.state = optsBrowsing
		case "y":
			return m, doUninstall(false)
		case "Y":
			return m, doUninstall(true)
		}
		return m, nil
	}

	// Browsing. The Ctrl keys the bottom menu advertises are handled here:
	// they are Config's own, not global, because ^r means Refresh on Home and
	// Regenerate on Plus. They were in the button table and in Help from the
	// start, but no screen ever handled them.
	switch k.String() {
	case "esc":
		if s.page != optsPageMain {
			(&m).setPage(optsPageMain)
			return m, nil
		}
		m.cur = m.optsReturn()
		m.optsState = nil
		return m, nil
	case "j", "down", "tab":
		m.optsIdx = nextSelectable(s.rows, m.optsIdx, +1)
	case "k", "up", "shift+tab":
		m.optsIdx = nextSelectable(s.rows, m.optsIdx, -1)
	case "ctrl+r":
		return m.optsReload()
	case "ctrl+t":
		return m.optsResetTheme()
	case "ctrl+x":
		return m.optsAskUninstall()
	case "enter":
		return m.activateOptsRow()
	}
	return m, nil
}

// saveOptsValue validates, applies and writes one key, then rebuilds the rows.
// Validation is not optional: PLUS_SUFFIX_LEN="abc" used to be accepted and
// silently become 5, and an invalid SHOW_ALIASES_FOR regexp degraded to a
// substring match without telling anyone.
func (m model) saveOptsValue(key, value string) (model, tea.Cmd) {
	s := m.optsState
	s.state = optsBrowsing
	if key == "" {
		return m, nil
	}
	if err := config.ValidateKey(key, value); err != nil {
		m.flash = ""
		m.errMsg = err.Error()
		return m, nil
	}
	m.applyEdit(key, value)
	if key == "TUTA_PROXY" {
		// Valid (checked above); new clients must use it right away.
		_ = api.SetProxy(m.cfg.TutaProxy)
	}
	if err := config.Save(m.cfg); err != nil {
		m.errMsg = "save: " + err.Error()
		logx.Errorf("save config (%s): %v", key, err)
	} else {
		m.errMsg = ""
		m.flash = "saved " + key
		// LogValue, not the raw value: TUTA_PROXY can carry credentials and
		// the log is shared (logs, the Logs screen, the .1 rotation).
		logx.Infof("config %s = %q", key, config.LogValue(key, config.ToMap(m.cfg)[key]))
	}
	s.rows = optsRowsFor(m.cfg, s.page)
	return m, nil
}

// applyEdit sets one key on a COPY of the config: Fetch/login goroutines may
// still hold the old *config.Config and read it concurrently. The key-to-field
// mapping lives in internal/config, so this screen cannot know about a key
// the file does not.
func (m *model) applyEdit(key, value string) {
	cfg := *m.cfg
	m.cfg = &cfg
	// Live preview: a theme row must repaint as it is changed, and a changed
	// LOG_LEVEL has to take effect without a restart.
	defer func() { applyStyle(m.cfg); logx.Setup(m.cfg) }()
	_ = config.Apply(m.cfg, key, value)
}

func (m model) activateOptsRow() (model, tea.Cmd) {
	s := m.optsState
	if m.optsIdx >= len(s.rows) {
		return m, nil
	}
	row := s.rows[m.optsIdx]
	switch row.action {
	case "toggle":
		cur := strings.Contains(row.value, "x")
		return m.saveOptsValue(row.key, fmt.Sprintf("%t", !cur))
	case "edit":
		m.openOptsEditor(stripDashed(row.value))
	case "choice":
		choices := row.choices
		cur := config.ToMap(m.cfg)[row.key]
		if cur != "" && !contains(choices, cur) {
			// A key whose file syntax is wider than the list the screen
			// offers: CACHE_TTL takes any Go duration while the list holds
			// the five common ones. Without the current value as an entry of
			// its own, indexOf fell back to 0 and confirming the chooser
			// replaced a hand-written "45m" with "8h".
			choices = append([]string{cur}, choices...)
		}
		s.radio = choices
		s.radioIdx = indexOf(choices, cur)
		s.state = optsChoosing
	case "change-domain":
		// Reuse the domain list already loaded by the dashboard. With no
		// session (so no domains), fall back to a free-text edit.
		if len(m.domains) == 0 {
			m.openOptsEditor(stripDashed(row.value))
			return m, nil
		}
		// With an empty entry first: DEFAULT_DOMAIN is allowed to be empty
		// (the Create-alias field then falls back to the first domain), and
		// the chooser had no way to get back to that once a domain was
		// picked. The empty value is what the file stores; the label only
		// says so on screen.
		s.radio = append([]string{""}, m.domains...)
		s.radioIdx = indexOf(s.radio, m.cfg.DefaultDomain)
		s.state = optsChoosing
	case "update-check":
		m.flash = "checking " + m.cfg.UpdateRef + "…"
		m.errMsg = ""
		return m, updateCheckCmd(m.cfg.UpdateRef)
	case "update-now":
		// The same `tuta-alias update` the CLI runs, with the terminal handed
		// over: a rebuild takes minutes and its output is the whole point.
		return m, updateNowCmd()
	case "vault-info":
		if vault.Exists() {
			m.flash = "vault at " + shortenHome(vault.Path()) + " — `tuta-alias vault set` replaces it, `vault rm` deletes it"
		} else {
			m.flash = "no vault yet — `tuta-alias vault set`, or tick \"Save login\" on the login form"
		}
	case "clear-cache":
		key := cacheKeyFor(m.cfg)
		if key != "" {
			if err := creds.Clear(key); err != nil {
				m.errMsg = "clear cache: " + err.Error()
			} else if m.cfg.CredBackend == "keyring" {
				m.flash = "saved login deleted"
			} else {
				m.flash = "cache cleared"
			}
			s.rows = optsRowsFor(m.cfg, s.page)
		}
	case "copy-token":
		tok, err := serve.GetOrCreateToken()
		if err != nil {
			m.errMsg = "api token: " + err.Error()
			return m, nil
		}
		if _, err := copyToClipboard(tok); err != nil {
			m.errMsg = "clipboard: " + err.Error()
			return m, nil
		}
		// Say where it went: the clipboard is readable by every program in
		// the session, so this is not a neutral place to leave a bearer token.
		m.flash = "API token copied to the clipboard — clear it when you are done"
		s.rows = optsRowsFor(m.cfg, s.page)
	case "rotate-token":
		tok, err := serve.RotateToken()
		if err != nil || tok == "" {
			m.errMsg = "rotate token: " + errText(err)
			return m, nil
		}
		m.flash = "API token rotated — update your clients (a running serve picks it up on the next request)"
		s.rows = optsRowsFor(m.cfg, s.page)
	case "open-theme":
		(&m).setPage(optsPageTheme)
	case "back-page":
		(&m).setPage(optsPageMain)
	case "doctor":
		(&m).openDoctor()
	case "logs":
		(&m).openLogs()
	case "edit-config":
		return m, openEditorCmd()
	case "reload":
		return m.optsReload()
	case "resettheme":
		return m.optsResetTheme()
	case "uninstall":
		return m.optsAskUninstall()
	}
	return m, nil
}

func (m *model) openOptsEditor(value string) {
	ti := textinput.New()
	ti.SetValue(value)
	ti.Focus()
	ti.CharLimit = 256
	m.optsState.input = ti
	m.optsState.state = optsEditing
}

// optsReload re-reads the file from disk (^r / the Reload entry).
func (m model) optsReload() (model, tea.Cmd) {
	cfg, err := config.Load()
	if err != nil {
		m.errMsg = "reload: " + err.Error()
		return m, nil
	}
	m.cfg = cfg
	applyStyle(cfg)
	m.optsState.rows = optsRowsFor(m.cfg, m.optsState.page)
	m.optsState.state = optsBrowsing
	m.flash = "config reloaded"
	m.errMsg = reapplyProxy(cfg)
	return m, nil
}

// optsResetTheme restores the Theme group and nothing else (^t / Reset theme).
func (m model) optsResetTheme() (model, tea.Cmd) {
	cfg := *m.cfg
	config.ResetTheme(&cfg)
	m.cfg = &cfg
	applyStyle(m.cfg)
	if err := config.Save(m.cfg); err != nil {
		m.errMsg = "save: " + err.Error()
	} else {
		m.errMsg = ""
		m.flash = "theme reset to defaults"
	}
	m.optsState.rows = optsRowsFor(m.cfg, m.optsState.page)
	m.optsState.state = optsBrowsing
	return m, nil
}

// optsAskUninstall opens the y/Y/n confirmation (^x / Uninstall).
func (m model) optsAskUninstall() (model, tea.Cmd) {
	m.optsState.state = optsConfirmingUninstall
	return m, nil
}

func errText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

// reapplyProxy hands a reloaded TUTA_PROXY to internal/api. An invalid value
// makes every request fail (never direct); the returned text says why.
func reapplyProxy(cfg *config.Config) string {
	if err := api.SetProxy(cfg.TutaProxy); err != nil {
		return err.Error()
	}
	return ""
}

func stripDashed(s string) string {
	// values like "(not set)" rendered through mutedStyle still contain
	// ANSI escapes; just strip them to "" for editing.
	if strings.Contains(s, "(not set)") || strings.Contains(s, "(default)") ||
		strings.Contains(s, "(not created yet)") {
		return ""
	}
	return s
}

func nextSelectable(rows []optsRow, cur, dir int) int {
	for i := cur + dir; i >= 0 && i < len(rows); i += dir {
		if rows[i].selectable() {
			return i
		}
	}
	return cur
}

// contains is "is this value one of the offered ones", which indexOf cannot
// answer: it returns 0 both for the first entry and for no match.
func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func indexOf(xs []string, want string) int {
	for i, x := range xs {
		if x == want {
			return i
		}
	}
	return 0
}

// openEditorCmd runs $EDITOR via tea.ExecProcess and reloads config after.
// The editor choice lives in internal/config so the TUI and
// `tuta-alias config edit` cannot disagree about it.
func openEditorCmd() tea.Cmd {
	return tea.ExecProcess(
		execCmd(config.Editor(), config.Path()),
		func(err error) tea.Msg { return reloadAfterEditMsg{err: err} },
	)
}

// reloadAfterEditMsg is sent by openEditorCmd's callback so Update can
// reload the config in the next frame.
type reloadAfterEditMsg struct{ err error }

// uninstallDoneMsg carries what removeInstall printed, so the lines land
// AFTER bubbletea has given the terminal back (Run prints them). Written to
// os.Stderr from inside the command they landed in the middle of a frame.
type uninstallDoneMsg struct{ note string }

// doUninstall removes binary + completion files; if purge, also config +
// session + token + client-version cache + keyring entries.
func doUninstall(purge bool) tea.Cmd {
	return func() tea.Msg {
		var b strings.Builder
		removeInstall(&b, purge)
		return uninstallDoneMsg{note: b.String()}
	}
}

// viewOptions renders the Options screen.
func (m model) viewOptions() string {
	s := m.optsState
	if s == nil {
		// Render-only fallback: a screen must never be able to show a
		// placeholder instead of itself.
		s = newOptsState(m.cfg)
	}

	var b strings.Builder
	b.WriteString(m.banner())
	b.WriteString("\n\n")
	title := "Config"
	if s.page == optsPageTheme {
		title = "Config › Theme"
	}
	b.WriteString(screenTitle(title))
	b.WriteString("\n\n")

	lines := make([]string, 0, len(s.rows))
	sel := 0
	for i, r := range s.rows {
		switch r.kind {
		case "header":
			lines = append(lines, indent+okStyle.Render(r.label))
		case "info":
			lines = append(lines, "  "+mutedStyle.Render(r.label+" ")+txt(r.value))
		case "blank":
			lines = append(lines, "")
		case "field":
			marker := "  "
			if i == m.optsIdx {
				marker = iconStyle.Render("▶ ")
				sel = len(lines)
			}
			val := r.value
			if i == m.optsIdx && s.state == optsEditing {
				val = s.input.View()
			}
			lines = append(lines, marker+txt(fmt.Sprintf("%-18s", r.label+":"))+" "+txt(val))
		case "action":
			marker := "    "
			if i == m.optsIdx {
				marker = "  " + iconStyle.Render("▶ ")
				sel = len(lines)
			}
			lines = append(lines, marker+bracketStyle.Render("[")+txt(r.label)+bracketStyle.Render("]"))
		}
	}

	// The screen is taller than a terminal once every key has a row, so it
	// has to scroll or the bottom menu falls off the bottom.
	bottom := m.renderBottom()
	if room := m.height - countLines(b.String()) - countLines(bottom) - 6; m.height > 0 && room > 4 {
		lines = scrollWindow(lines, sel, room)
	}
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n")

	if s.state == optsChoosing {
		b.WriteString("\n")
		b.WriteString(mutedStyle.Render("Choose:"))
		b.WriteString("\n")
		for i, c := range s.radio {
			marker := bracketStyle.Render("  ( ) ")
			if i == s.radioIdx {
				marker = iconStyle.Render("  (•) ")
			}
			label := c
			if label == "" {
				label = "(none)"
			}
			b.WriteString(marker + txt(label) + "\n")
		}
		b.WriteString(mutedStyle.Render("↑/↓ to move  ·  Enter to choose  ·  Esc to cancel"))
		b.WriteString("\n")
	}

	if s.state == optsConfirmingUninstall {
		b.WriteString("\n")
		b.WriteString(errStyle.Render("Uninstall:"))
		b.WriteString("\n")
		b.WriteString("  " + txt("y = remove the binary + completion files (keep config, + addresses, notes, session)") + "\n")
		b.WriteString("  " + txt("Y = purge: also remove config (incl. + addresses and alias notes), session,") + "\n")
		b.WriteString("      " + txt("api-token, client-version cache and keyring entries") + "\n")
		b.WriteString("  " + txt("n / Esc = cancel") + "\n")
	}

	// Status + the shared bottom menu; the navigation keys live in Help now.
	b.WriteString("\n" + bottom + "\n")
	return b.String()
}

// countLines counts the rendered lines of s.
func countLines(s string) int { return strings.Count(s, "\n") }
