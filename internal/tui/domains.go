// domains.go — the Domains screen: the account's custom domains, with the DNS
// check and the ownership TXT value behind a keypress. It is the TUI side of
// `tuta-alias domains`, `dns-check` and `verify-record`, and it calls the same
// services functions the CLI does (internal/cliflow/read.go), so the two
// cannot disagree about what a domain's DNS looks like.
//
// Like Doctor it is read-only and opened from another screen; unlike Doctor
// it has a cursor, because the checks are per domain.
package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tdwgm/tuta-alias/internal/doctor"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/services"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// checkDomainDNS is the one network call the screen makes besides listing
// domains. It is a var so the tests can answer without a network; the default
// is exactly what `tuta-alias dns-check` runs.
var checkDomainDNS = func(s *session.Session, domain string) (*services.DNSCheck, error) {
	return services.CheckDomainDNS(client(s), domain)
}

// domainsRefreshingFlash is the line the screen's own refresh shows. It is a
// constant because app.go clears it when the fetch lands, and a flash that
// nothing clears is a lie about what the program is doing.
const domainsRefreshingFlash = "refreshing domains…"

// domainCheckMsg carries one domain's DNS verdict back from its tea.Cmd.
type domainCheckMsg struct {
	domain string
	res    *services.DNSCheck
	err    error
}

func checkDomainCmd(s *session.Session, domain string) tea.Cmd {
	return func() tea.Msg {
		res, err := checkDomainDNS(s, domain)
		return domainCheckMsg{domain: domain, res: res, err: err}
	}
}

// domainResult is what is known about one domain after a check.
type domainResult struct {
	res      *services.DNSCheck
	err      error
	checking bool
}

// domainsState backs the Domains screen. nil on the model means the screen is
// not open.
type domainsState struct {
	list    []string
	cursor  int
	scroll  int
	results map[string]*domainResult
	loading bool // the domain list is being re-fetched
}

func newDomainsState(list []string) *domainsState {
	return &domainsState{
		list:    append([]string(nil), list...),
		results: map[string]*domainResult{},
	}
}

func domainsButtons() []button {
	return []button{
		{label: "Check DNS", action: "domains-check", key: "^k"},
		{label: "TXT record", action: "domains-txt", key: "^v"},
		{label: "Refresh", action: "domains-refresh", key: "^r"},
		{label: "Back", action: "back", key: "esc"},
		{label: "Help", action: "help", key: "^h"},
	}
}

// openDomains shows the screen. The list the dashboard already fetched is
// used as is; only when there is none yet is it fetched, so opening the
// screen right after login does not show an empty list that is merely late.
func (m *model) openDomains() tea.Cmd {
	if m.cur != screenDomains {
		m.subReturn = m.cur
	}
	m.domainsState = newDomainsState(m.domains)
	m.cur = screenDomains
	m.menuActive, m.menuSel = false, 0
	m.flash, m.errMsg = "", ""
	logx.Debugf("tui: domains")
	if len(m.domains) == 0 && m.sess != nil {
		m.domainsState.loading = true
		return fetchDomains(m.sess)
	}
	return nil
}

// ensureDomains is the state fallback for every path that reaches the screen
// without openDomains (a test, a future caller): a screen must not render a
// placeholder instead of itself.
func (m *model) ensureDomains() *domainsState {
	if m.domainsState == nil {
		m.domainsState = newDomainsState(m.domains)
	}
	return m.domainsState
}

// syncDomains folds a finished domain fetch into the open screen. A failed
// fetch keeps the list that is showing; the model-wide handler reports it.
func (m *model) syncDomains(err error) {
	d := m.domainsState
	if d == nil {
		return
	}
	d.loading = false
	if err != nil {
		return
	}
	d.list = append([]string(nil), m.domains...)
	d.cursor = min(d.cursor, max(len(d.list)-1, 0))
	d.clampScroll(m)
}

func (d *domainsState) selected() (string, bool) {
	if d.cursor < 0 || d.cursor >= len(d.list) {
		return "", false
	}
	return d.list[d.cursor], true
}

// recordStatus is cliflow.DNSCheck's classification: a record is matched on
// type and subdomain, not value, because a wrong value is what INVALID means.
func recordStatus(res *services.DNSCheck, r services.DNSRecord) string {
	has := func(list []services.DNSRecord) bool {
		for _, x := range list {
			if x.Type == r.Type && x.Subdomain == r.Subdomain {
				return true
			}
		}
		return false
	}
	switch {
	case has(res.MissingRecords):
		return "MISSING"
	case has(res.InvalidRecords):
		return "INVALID"
	}
	return "OK"
}

// verdict is the one-line summary shown beside a checked domain, with the
// doctor level that colours its mark.
func (r *domainResult) verdict() (doctor.Level, string) {
	switch {
	case r == nil:
		return doctor.LevelOK, ""
	case r.checking:
		return doctor.LevelOK, "checking…"
	case r.err != nil:
		return doctor.LevelFail, r.err.Error()
	}
	res := r.res
	switch {
	case res.CheckResult != "OK":
		return doctor.LevelFail, res.CheckResult
	case len(res.InvalidRecords) > 0:
		return doctor.LevelFail, fmt.Sprintf("%d invalid, %d missing", len(res.InvalidRecords), len(res.MissingRecords))
	case len(res.MissingRecords) > 0:
		return doctor.LevelWarn, fmt.Sprintf("%d record(s) missing", len(res.MissingRecords))
	}
	return doctor.LevelOK, "OK"
}

func levelStyle(l doctor.Level) func(...string) string {
	switch l {
	case doctor.LevelFail:
		return errStyle.Render
	case doctor.LevelWarn:
		return warnStyle.Render
	}
	return iconStyle.Render
}

// domainsLines is the body, and the index of the line each domain starts on
// (scrolling and the cursor both work in lines, because a checked domain is
// several of them).
func (m model) domainsLines() (lines []string, starts []int) {
	d := m.domainsState
	if d == nil {
		d = newDomainsState(m.domains)
	}
	if len(d.list) == 0 {
		msg := "No custom domains on this account."
		if d.loading {
			msg = "loading…"
		}
		return []string{indent + mutedStyle.Render(msg)}, nil
	}
	for i, dom := range d.list {
		starts = append(starts, len(lines))
		mark := "  "
		if i == d.cursor && !m.menuActive {
			mark = iconStyle.Render("▶ ")
		}
		r := d.results[dom]
		line := indent + mark + txt(dom)
		if r != nil {
			lvl, text := r.verdict()
			line = indent + mark + levelStyle(lvl)(doctor.Mark(lvl)) + " " + txt(fmt.Sprintf("%-24s", dom)) + " " + txt(text)
		}
		lines = append(lines, fitLine(line))
		if r == nil || r.res == nil || r.checking || r.err != nil {
			continue
		}
		for _, rec := range r.res.RequiredRecords {
			status := recordStatus(r.res, rec)
			style := okStyle.Render
			switch status {
			case "MISSING":
				style = warnStyle.Render
			case "INVALID":
				style = errStyle.Render
			}
			sub := rec.Subdomain
			if sub == "" {
				sub = "@"
			}
			lines = append(lines, fitLine(indent+"    "+style(fmt.Sprintf("%-8s", status))+" "+
				txt(fmt.Sprintf("%-7s %-16s", rec.Type, sub))+" "+mutedStyle.Render(rec.Value)))
		}
	}
	return lines, starts
}

func (m model) domainsMaxScroll() int {
	room := m.diagRoom()
	if room == 0 {
		return 0
	}
	lines, _ := m.domainsLines()
	return maxScroll(len(lines), room)
}

func (d *domainsState) clampScroll(m *model) {
	d.scroll = min(max(d.scroll, 0), m.domainsMaxScroll())
}

// reveal scrolls just far enough to keep the cursor's domain, and as much of
// its records as fit, on screen.
func (d *domainsState) reveal(m model) {
	room := m.diagRoom()
	if room == 0 || len(d.list) == 0 {
		return
	}
	lines, starts := m.domainsLines()
	inner := max(room, 5) - 2
	top := starts[d.cursor]
	end := len(lines) - 1
	if d.cursor+1 < len(starts) {
		end = starts[d.cursor+1] - 1
	}
	d.scroll = min(d.scroll, top)
	if end >= d.scroll+inner {
		d.scroll = min(top, end-inner+1)
	}
	d.scroll = min(max(d.scroll, 0), maxScroll(len(lines), room))
}

// domainsKey is the navigation half of the screen, called from globalKey like
// the Doctor and Logs scrolling it is modelled on. done=false lets the key
// fall through to the shared menu handling (↓ off the last row enters it).
func (m model) domainsKey(key string) (model, bool) {
	d := m.ensureDomains()
	last := len(d.list) - 1
	room := max(m.diagRoom(), 1)
	switch key {
	case "esc", "q":
		m.cur = m.subReturn
		m.domainsState = nil
		m.menuActive, m.menuSel = false, 0
		return m, true
	case "up", "k":
		if m.menuActive {
			return m, false
		}
		switch {
		case d.cursor > 0:
			d.cursor--
			d.reveal(m)
		case d.scroll > 0:
			d.scroll--
		}
		return m, true
	case "down", "j":
		if m.menuActive {
			return m, false
		}
		switch {
		case d.cursor < last:
			d.cursor++
			d.reveal(m)
		case d.scroll < m.domainsMaxScroll():
			d.scroll++
		default:
			return m, false
		}
		return m, true
	case "pgup":
		d.cursor = max(d.cursor-room, 0)
		d.reveal(m)
		return m, true
	case "pgdown", " ":
		d.cursor = min(d.cursor+room, max(last, 0))
		d.reveal(m)
		return m, true
	case "home":
		d.cursor, d.scroll = 0, 0
		return m, true
	case "end":
		d.cursor = max(last, 0)
		d.reveal(m)
		d.scroll = m.domainsMaxScroll()
		return m, true
	}
	return m, false
}

// updateDomains handles what globalKey leaves: the actions and the results of
// the commands they start.
func (m model) updateDomains(msg tea.Msg) (model, tea.Cmd) {
	d := m.ensureDomains()
	switch msg := msg.(type) {
	case domainCheckMsg:
		if mm, cmd, done := m.routeIfExpired(msg.err); done {
			return mm, cmd
		}
		r := d.results[msg.domain]
		if r == nil {
			r = &domainResult{}
			d.results[msg.domain] = r
		}
		r.checking, r.res, r.err = false, msg.res, msg.err
		if msg.err != nil {
			logx.Errorf("dns check %s: %v", msg.domain, msg.err)
			m.errMsg = fmt.Sprintf("dns check %s failed: %v", msg.domain, msg.err)
			return m, nil
		}
		_, text := r.verdict()
		m.flash = fmt.Sprintf("%s: %s", msg.domain, text)
		d.reveal(m)
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "enter", "ctrl+k":
			return m.domainsCheck()
		case "ctrl+v":
			return m.domainsCopyTXT()
		case "ctrl+r":
			return m.domainsRefresh()
		}
	}
	return m, nil
}

func (m model) domainsCheck() (model, tea.Cmd) {
	d := m.ensureDomains()
	dom, ok := d.selected()
	if !ok {
		m.flash, m.errMsg = "", "no domain selected"
		return m, nil
	}
	if r := d.results[dom]; r != nil && r.checking {
		m.flash, m.errMsg = "", "already checking "+dom
		return m, nil
	}
	r := d.results[dom]
	if r == nil {
		r = &domainResult{}
		d.results[dom] = r
	}
	r.checking = true
	m.flash, m.errMsg = "checking "+dom+"…", ""
	return m, checkDomainCmd(m.sess, dom)
}

// domainsCopyTXT copies the ownership record. It is computed locally from the
// customer id (services.DomainValidationRecord), so it needs no network and
// works before the domain is verified, which is when it is wanted.
func (m model) domainsCopyTXT() (model, tea.Cmd) {
	d := m.ensureDomains()
	dom, ok := d.selected()
	if !ok {
		m.flash, m.errMsg = "", "no domain selected"
		return m, nil
	}
	if m.sess == nil || m.sess.CustomerID == "" {
		m.flash, m.errMsg = "", "session has no customer_id; log in again"
		return m, nil
	}
	rec := services.DomainValidationRecord(dom, m.sess.CustomerID)
	tool, err := copyToClipboard(rec)
	if err != nil {
		logx.Errorf("clipboard: %v", err)
		m.flash, m.errMsg = "", "clipboard: "+err.Error()
		return m, nil
	}
	m.flash, m.errMsg = fmt.Sprintf("copied TXT for %s: %s (%s)", dom, rec, tool), ""
	return m, nil
}

func (m model) domainsRefresh() (model, tea.Cmd) {
	d := m.ensureDomains()
	if m.sess == nil {
		return m, nil
	}
	d.loading = true
	m.flash, m.errMsg = domainsRefreshingFlash, ""
	return m, fetchDomains(m.sess)
}

// viewDomains renders the screen.
func (m model) viewDomains() string {
	d := m.domainsState
	if d == nil {
		d = newDomainsState(m.domains)
		m.domainsState = d
	}

	var b strings.Builder
	b.WriteString(m.banner() + "\n\n")
	b.WriteString(screenTitle("Domains") + "\n")
	b.WriteString(indent + mutedStyle.Render(fmt.Sprintf("%d custom domain(s)  ·  Enter checks the DNS", len(d.list))) + "\n\n")

	lines, _ := m.domainsLines()
	if room := m.diagRoom(); room > 0 && len(lines) > room {
		lines = windowAt(lines, d.scroll, room)
	}
	b.WriteString(strings.Join(lines, "\n") + "\n")
	b.WriteString("\n" + m.renderBottom())
	return b.String()
}
