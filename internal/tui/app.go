// Package tui is the bubbletea-based default mode.
package tui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/doctor"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/services"
	"github.com/tdwgm/tuta-alias/internal/session"
	"github.com/tdwgm/tuta-alias/internal/update"
)

// copyToClipboard shells out to wl-copy (Wayland) or pbcopy (macOS) or xclip.
// It is a var so tests can swap in a stub instead of touching the real
// clipboard; restore the previous value with t.Cleanup.
var copyToClipboard = func(s string) (string, error) {
	for _, candidate := range []string{"wl-copy", "pbcopy", "xclip"} {
		if path, err := exec.LookPath(candidate); err == nil {
			c := exec.Command(path)
			if candidate == "xclip" {
				c = exec.Command(path, "-selection", "clipboard")
			}
			c.Stdin = strings.NewReader(s)
			if err := c.Run(); err == nil {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("no clipboard helper found")
}

type browseRow struct {
	Domain  string
	Addr    string
	Enabled bool
	Primary bool
}

func flattenAliases(grouped map[string][]services.AddressEntry) []browseRow {
	doms := make([]string, 0, len(grouped))
	for d := range grouped {
		doms = append(doms, d)
	}
	sort.Strings(doms)
	out := []browseRow{}
	for _, d := range doms {
		for _, a := range grouped[d] {
			out = append(out, browseRow{d, a.MailAddress, a.Enabled, a.Primary})
		}
	}
	return out
}

// browseWindow returns the [start,end) row range to render so that idx
// stays visible. height is the terminal height; ~8 lines are reserved for
// the title, domain headers, footer, flash, and "more" indicators. When
// height is unknown (0) or everything fits, the full range is returned.
func browseWindow(idx, total, height int) (int, int) {
	visible := height - 8
	if height <= 0 || visible < 3 {
		visible = total // no/short size info → render all (terminal scrolls)
	}
	if total <= visible {
		return 0, total
	}
	start := idx - visible/2
	if start < 0 {
		start = 0
	}
	if start > total-visible {
		start = total - visible
	}
	return start, start + visible
}

type screen int

const (
	screenHome screen = iota
	screenLogin
	screenOptions
	screenPlus
	screenHelp
	screenDoctor  // the doctor checklist, opened from Config
	screenLogs    // the tail of the log file, opened from Config
	screenDomains // the custom domains and their DNS, opened from Home
)

type model struct {
	cur    screen
	sess   *session.Session
	cfg    *config.Config
	width  int
	height int
	flash  string

	domains []string
	aliases map[string][]services.AddressEntry
	quota   *services.Quota
	loading string
	errMsg  string

	// Home dashboard
	naInput     textinput.Model // "New alias" field
	searchInput textinput.Model // alias search/filter field
	homeFocus   int             // homeFocusNew | homeFocusSearch | homeFocusList
	homeReady   bool            // dashboard inputs initialised

	// Bottom menu (TUI Scaffold standard B3): menuActive means the menu has
	// focus, so ←/→ move between entries and the selected one glitters.
	// helpReturn is the screen Help was opened from, so Esc goes back there
	// rather than to a fixed screen.
	menuSel    int
	menuActive bool
	helpReturn screen
	// helpScroll is the first visible line of the Help body: the generated
	// text is taller than a normal terminal, so it scrolls with up/down.
	helpScroll int

	// Browse / alias list (shared by the dashboard)
	browseRows []browseRow
	browseIdx  int

	// Home list interaction
	listAll bool // "List aliases" expanded (show the full filtered set)
	// searchRefreshed is set once the first search keystroke has re-fetched
	// the alias list, so a long-open TUI never searches a stale list.
	searchRefreshed bool

	// Delete confirmation (dashboard): require typing the exact address.
	confirmDelete bool
	deleteTarget  string
	confirmInput  textinput.Model

	// Per-alias notes (local store) + the note editor.
	aliasNotes map[string]string
	noteEdit   bool
	noteTarget string
	noteInput  textinput.Model

	// Options
	optsIdx   int
	optsState *optsState

	// Plus (sub-address generator)
	plusState *plusState

	// Login
	loginState *loginState

	// exitNote is printed by Run after bubbletea has restored the terminal
	// (uninstall's report).
	exitNote string

	// Doctor / Logs (diag.go). subReturn is the screen they were opened from.
	subReturn    screen
	doctorReport doctor.Report
	logTail      []string
	diagScroll   int

	// Domains (domains.go); nil means the screen is not open.
	domainsState *domainsState
}

// liveLine is the live info block the standard puts directly under the banner
// rule (LIVE_INFO: full | compact | off). LIVE_INFO and COLOR_LIVE were config
// keys that painted nothing: renderBannerLive existed, and every screen called
// renderBanner() with no live data at all.
func (m model) liveLine() string {
	if uiLiveInfo == "off" {
		return ""
	}
	who := "not logged in"
	if m.sess != nil && m.sess.MailAddress != "" {
		who = m.sess.MailAddress
	}
	part := func(label, value string) string {
		return mutedStyle.Render(label+" ") + liveStyle.Render(value)
	}
	parts := []string{part("account", who)}
	if n := len(m.browseRows); n > 0 {
		parts = append(parts, part("aliases", fmt.Sprintf("%d", n)))
	}
	if uiLiveInfo == "full" {
		if m.quota != nil {
			parts = append(parts, part("quota", fmt.Sprintf("%d/%d", m.quota.UsedAliases, m.quota.TotalAliases)))
		}
		dom := m.cfg.DefaultDomain
		if dom == "" {
			dom = "—"
		}
		parts = append(parts, part("default", dom))
		proxy := "direct"
		if m.cfg.TutaProxy != "" {
			if u, err := api.ParseProxy(m.cfg.TutaProxy); err == nil {
				proxy = u.Redacted()
			} else {
				proxy = "invalid"
			}
		}
		parts = append(parts, part("network", proxy))
	}
	return fitLine(indent + strings.Join(parts, mutedStyle.Render("  ·  ")))
}

// banner is the banner with this screen's live block under it.
func (m model) banner() string { return renderBannerLive(m.liveLine()) }

func client(s *session.Session) *api.Client {
	return api.NewClient(consts.BaseURL, s.AccessToken)
}

func initialModel() model {
	cfg, _ := config.Load()
	if cfg == nil {
		cfg = config.Default()
	}
	applyStyle(cfg)
	m := model{cfg: cfg, cur: screenHome}
	if s, err := session.Load(); err == nil {
		m.sess = s
	} else {
		// No session → land on login screen.
		m.cur = screenLogin
		m.loginState = newLoginState(cfg)
	}
	return m
}

// updateCheckedMsg carries the result of an update check made off the UI loop.
type updateCheckedMsg struct {
	st  update.Status
	err error
	// auto marks a check that AUTO_UPDATE started rather than the user, so a
	// failure stays quiet: a laptop with no network must not greet you with
	// an error you did not ask for.
	auto bool
}

// updateDoneMsg is sent after `tuta-alias update` has had the terminal.
type updateDoneMsg struct{ err error }

// updateCheckCmd asks the remote what the ref points at now.
func updateCheckCmd(ref string) tea.Cmd {
	return func() tea.Msg {
		st, err := update.Check(ref)
		return updateCheckedMsg{st: st, err: err}
	}
}

// autoUpdateCmd is the AUTO_UPDATE check on start. It uses the cached result
// until UPDATE_INTERVAL has passed, so opening the TUI is not a network call.
func autoUpdateCmd(cfg *config.Config) tea.Cmd {
	if cfg == nil || cfg.AutoUpdate == "off" {
		return nil
	}
	ref, interval := cfg.UpdateRef, cfg.UpdateInterval
	return func() tea.Msg {
		st, err := update.CheckCached(ref, interval)
		return updateCheckedMsg{st: st, err: err, auto: true}
	}
}

// updateNowCmd hands the terminal to `tuta-alias update`, which is the same
// code path the CLI runs, so the two cannot drift.
func updateNowCmd() tea.Cmd {
	self, err := os.Executable()
	if err != nil {
		return func() tea.Msg { return updateDoneMsg{err: err} }
	}
	return tea.ExecProcess(execCmd(self, "update"), func(err error) tea.Msg {
		return updateDoneMsg{err: err}
	})
}

// --- Messages from background fetches ---

type domainsMsg struct {
	doms []string
	err  error
}
type aliasesMsg struct {
	m   map[string][]services.AddressEntry
	err error
}
type quotaMsg struct {
	q   *services.Quota
	err error
}
type opDoneMsg struct {
	verb string
	addr string
	err  error
}

// verbCreated is the opDoneMsg verb for alias creation. Named because the
// Update loop keys the auto-copy on it; "deleted" stays a literal.
const verbCreated = "created"

func fetchDomains(s *session.Session) tea.Cmd {
	return func() tea.Msg {
		doms, err := services.ListDomains(client(s), s)
		return domainsMsg{doms, err}
	}
}
func fetchAliases(s *session.Session) tea.Cmd {
	return func() tea.Msg {
		out, err := services.AliasesByDomain(client(s), s)
		return aliasesMsg{out, err}
	}
}
func fetchQuota(s *session.Session) tea.Cmd {
	return func() tea.Msg {
		q, err := services.AliasQuota(client(s), s)
		return quotaMsg{q, err}
	}
}
func opCreate(s *session.Session, addr string) tea.Cmd {
	return func() tea.Msg {
		err := services.AddAlias(client(s), s, addr)
		return opDoneMsg{verbCreated, addr, err}
	}
}
func opDelete(s *session.Session, addr string) tea.Cmd {
	return func() tea.Msg {
		err := services.RemoveAlias(client(s), s, addr, false)
		return opDoneMsg{"deleted", addr, err}
	}
}

// opRestore re-enables a disabled alias (`tuta-alias delete --restore`). The
// TUI could disable an alias and showed "[disabled]" next to it, but had no
// way back: the only way to undo it was the CLI.
func opRestore(s *session.Session, addr string) tea.Cmd {
	return func() tea.Msg {
		err := services.RemoveAlias(client(s), s, addr, true)
		return opDoneMsg{"restored", addr, err}
	}
}

func (m model) Init() tea.Cmd {
	// No session but a credential backend is configured → try a no-prompt
	// auto-login (uses a cached/unlocked session if present; otherwise the
	// probe routes to the master-password screen).
	if m.sess == nil {
		if b := autoLoginBackend(m.cfg); b != nil {
			return tea.Batch(savedEmailCmd(), autoLoginCmd(m.cfg, b), autoUpdateCmd(m.cfg))
		}
		return tea.Batch(savedEmailCmd(), autoUpdateCmd(m.cfg))
	}
	// Logged in → populate the dashboard, and look for a newer build when
	// AUTO_UPDATE asks for it.
	return tea.Batch(m.loadHomeCmd(), autoUpdateCmd(m.cfg))
}

// routeIfExpired clears the session and routes to the login screen (kicking
// off auto-login when a backend is configured) if err indicates an expired
// session. Returns handled=true when it took over.
func (m model) routeIfExpired(err error) (model, tea.Cmd, bool) {
	if err == nil || !api.IsSessionExpired(err) {
		return m, nil, false
	}
	if m.sess == nil {
		// Already routed to login by an earlier expired response (loadHomeCmd
		// fires several fetches at once) — swallow the duplicates.
		return m, nil, true
	}
	_, _ = session.Clear()
	m.sess = nil
	m.loading = ""
	m.flash = ""
	m.cur = screenLogin
	m.loginState = newLoginState(m.cfg)
	if b := autoLoginBackend(m.cfg); b != nil {
		m.loginState.info = reauthInfo
		return m, tea.Batch(savedEmailCmd(), autoLoginCmd(m.cfg, b)), true
	}
	m.loginState.err = "session expired — please log in again"
	return m, savedEmailCmd(), true
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// layout.go's fitLine/wrapText read the package-level termWidth, not
		// the model. Leaving it at 0 meant nothing was ever truncated and
		// prose wrapped at the 40-column floor on a wide terminal.
		termWidth = msg.Width
		// A taller window means less to scroll. The offsets were left where
		// they were, and windowAt clamps the render, so the page looked right
		// while up-arrow did nothing for as many presses as the stale offset,
		// and atLastRow() sent down-arrow straight into the menu.
		m.helpScroll = min(m.helpScroll, m.helpMaxScroll())
		m.diagScroll = min(m.diagScroll, m.diagMaxScroll())
		return m, nil
	case menuTickMsg:
		// Only keep ticking while the menu has focus; an idle screen must not
		// wake the program 11 times a second.
		if !m.menuActive {
			return m, nil
		}
		menuFrame++
		return m, menuTickCmd()
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		if mm, cmd, done := m.globalKey(msg.String()); done {
			return mm, cmd
		}
	case savedEmailMsg:
		// Handled here, not in updateLogin, so the result lands even while
		// Options is open on top of a login screen.
		if m.loginState != nil {
			m.loginState.savedEmail = msg.email
		}
		return m, nil
	case loginDoneMsg, credsDoneMsg, savedLoginMsg, autoLoginResultMsg, autoLoginSkippedMsg:
		// Same reason as savedEmailMsg, and it matters more here: Help,
		// Config, Doctor and Logs are all reachable without a session, so the
		// no-session guard below deliberately does not reroute to login while
		// one of them is open. Routed through that guard, a login result
		// arriving in the meantime would be dropped — updateOptions and
		// updateDiag ignore everything that is not a key and screenHelp has no
		// case at all — and the form would stay in loginPhaseSubmitting, where
		// every key is ignored, with no way out but ^q.
		mm, cmd := m.updateLogin(msg)
		return mm, cmd
	case sessionClosedMsg:
		// Routed here for the same reason as the login results above. The close
		// ends wherever the user has gone since the logout or login that
		// started it, Help or Config as likely as not, and no screen's handler
		// takes it: the flash would say "closing…" for good, and a failure
		// would never be shown.
		return m.sessionClosed(msg), nil
	case spinner.TickMsg:
		// The login spinner. A tick that arrives after the form is gone has
		// nothing to animate and must not rebuild the login state.
		if m.loginState == nil {
			return m, nil
		}
		mm, cmd := m.updateLogin(msg)
		return mm, cmd
	case domainCheckMsg:
		// Routed here for the same reason as the login results below: Help,
		// Config, Doctor and Logs can all be opened over the Domains screen
		// while a DNS check is in flight, and the screen switch further down
		// only reaches updateDomains when Domains is the current screen. The
		// dropped result left the row saying "checking…" for good, and every
		// later check answered "already checking" with no command.
		mm, cmd := m.updateDomains(msg)
		return mm, cmd
	case domainsMsg:
		if mm, cmd, done := m.routeIfExpired(msg.err); done {
			return mm, cmd
		}
		if msg.err != nil {
			// Keep the list we had: overwriting it with the empty result of a
			// failed fetch blanked the Home "Domains (N)" count and the
			// fallback domain the Create-alias field uses.
			m.errMsg = msg.err.Error()
			logx.Errorf("fetch domains: %v", msg.err)
		} else {
			m.domains = msg.doms
		}
		(&m).syncDomains(msg.err)
		// The Domains screen's own refresh says "refreshing domains…" in the
		// flash, and nothing cleared it: syncDomains clears the screen's
		// loading flag, so the line stayed under a list that was already
		// refreshed. Home's equivalent uses m.loading, which aliasesMsg
		// clears.
		if m.flash == domainsRefreshingFlash {
			m.flash = ""
		}
		// loading ("refreshing…") is cleared by aliasesMsg: domains/quota can
		// land first and would otherwise hide a still-running alias fetch.
		return m, nil
	case aliasesMsg:
		if mm, cmd, done := m.routeIfExpired(msg.err); done {
			return mm, cmd
		}
		m.aliases = msg.m
		m.browseRows = flattenAliases(msg.m)
		if m.browseIdx >= len(m.browseRows) {
			m.browseIdx = 0
		}
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			logx.Errorf("fetch aliases: %v", msg.err)
		}
		m.loading = ""
		return m, nil
	case quotaMsg:
		if mm, cmd, done := m.routeIfExpired(msg.err); done {
			return mm, cmd
		}
		m.quota = msg.q
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			logx.Errorf("fetch quota: %v", msg.err)
		}
		return m, nil
	case updateCheckedMsg:
		if msg.err != nil {
			if !msg.auto {
				m.errMsg = msg.err.Error()
			}
			logx.Errorf("update check: %v", msg.err)
			return m, nil
		}
		switch {
		case msg.st.Err != "":
			if !msg.auto {
				m.errMsg = msg.st.Err
			}
		case msg.st.Newer && m.cfg.AutoUpdate == "install" && msg.auto:
			// "install" is the mode that does not ask.
			m.flash = m.cfg.UpdateRef + " points at a different build, updating…"
			return m, updateNowCmd()
		case msg.st.Newer:
			m.flash = msg.st.Ref + " points at a different build — Config › Update now"
		case !msg.auto:
			m.flash = "this is the build " + msg.st.Ref + " points at"
		}
		if m.optsState != nil {
			m.optsState.rows = optsRowsFor(m.cfg, m.optsState.page)
		}
		return m, nil
	case updateDoneMsg:
		if msg.err != nil {
			m.errMsg = "update: " + msg.err.Error()
		} else {
			m.flash = "update finished — restart tuta-alias to run the new build"
		}
		if m.optsState != nil {
			m.optsState.rows = optsRowsFor(m.cfg, m.optsState.page)
		}
		return m, nil
	case uninstallDoneMsg:
		m.exitNote = msg.note
		return m, tea.Quit
	case reloadAfterEditMsg:
		if cfg, err := config.Load(); err == nil {
			m.cfg = cfg
			applyStyle(cfg)
			if m.optsState != nil {
				m.optsState.rows = buildOptsRows(m.cfg)
			}
			m.flash = "config reloaded"
			m.errMsg = reapplyProxy(cfg)
		}
		return m, nil
	case opDoneMsg:
		if mm, cmd, done := m.routeIfExpired(msg.err); done {
			return mm, cmd
		}
		if msg.err != nil {
			m.errMsg = fmt.Sprintf("%s %s failed: %v", msg.verb, msg.addr, msg.err)
			logx.Errorf("%s %s: %v", msg.verb, msg.addr, msg.err)
		} else {
			logx.Infof("%s %s", msg.verb, msg.addr)
			m.flash = fmt.Sprintf("%s: %s", msg.verb, msg.addr)
			if msg.verb == verbCreated {
				// A fresh alias is almost always about to be pasted somewhere,
				// so it is copied automatically. The alias exists
				// either way — a clipboard failure is reported separately and
				// must not turn the success flash into an error.
				if _, err := copyToClipboard(msg.addr); err == nil {
					m.flash = fmt.Sprintf("%s: %s (copied)", msg.verb, msg.addr)
				} else {
					m.errMsg = "clipboard: " + err.Error()
					logx.Errorf("clipboard: %v", err)
				}
			}
			// Refresh aliases after mutation.
			m.loading = "refreshing…"
			return m, fetchAliases(m.sess)
		}
		return m, nil
	}
	// If we have no session, force-route to login regardless of cur.
	if m.sess == nil && m.cur != screenLogin && m.cur != screenOptions &&
		m.cur != screenDoctor && m.cur != screenLogs && m.cur != screenHelp {
		m.cur = screenLogin
		// updateLogin creates the login state (and its saved-email lookup).
		return m.updateLogin(msg)
	}
	switch m.cur {
	case screenHome:
		return m.updateHome(msg)
	case screenOptions:
		return m.updateOptions(msg)
	case screenPlus:
		return m.updatePlus(msg)
	case screenLogin:
		return m.updateLogin(msg)
	case screenDoctor, screenLogs:
		return m.updateDiag(msg)
	case screenDomains:
		return m.updateDomains(msg)
	}
	return m, nil
}

func (m model) View() string {
	// Options + Login are reachable without a session.
	if m.cur == screenOptions {
		return m.viewOptions()
	}
	if m.cur == screenLogin {
		return m.viewLogin()
	}
	// Help before the session guard: it explains how to log in, so it has to
	// be readable when there is no session yet. Doctor and Logs likewise —
	// "why can I not log in" is exactly when they are wanted.
	if m.cur == screenHelp {
		return m.viewHelp()
	}
	if m.cur == screenDoctor || m.cur == screenLogs {
		return m.viewDiag()
	}
	if m.sess == nil {
		// No session — fall through to the login screen view.
		return m.viewLogin()
	}
	switch m.cur {
	case screenHome:
		return m.viewHome()
	case screenPlus:
		return m.viewPlus()
	case screenDomains:
		return m.viewDomains()
	}
	return "(?)"
}

// Run is the cobra entry point. root is kept for the Help screen's command
// list, which is generated from cobra rather than hand-written.
func Run(stdout, stderr io.Writer, root *cobra.Command) int {
	SetCommandRoot(root)
	if cfg, err := config.Load(); err == nil {
		logx.Setup(cfg)
	}
	logx.Debugf("tui: start")
	final, err := tea.NewProgram(initialModel(), tea.WithOutput(stdout)).Run()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if m, ok := final.(model); ok && m.exitNote != "" {
		fmt.Fprint(stderr, m.exitNote)
	}
	return 0
}
