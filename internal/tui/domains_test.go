package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tdwgm/tuta-alias/internal/services"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// fakeDNS answers checkDomainDNS from a table and counts the calls, so a test
// can tell "a command was dispatched" from "the network was hit on the UI
// loop": the count must still be zero after Update returns.
func fakeDNS(t *testing.T, answers map[string]*services.DNSCheck, err error) *int {
	t.Helper()
	prev := checkDomainDNS
	t.Cleanup(func() { checkDomainDNS = prev })
	calls := 0
	checkDomainDNS = func(_ *session.Session, domain string) (*services.DNSCheck, error) {
		calls++
		if err != nil {
			return nil, err
		}
		return answers[domain], nil
	}
	return &calls
}

func domainsModel(t *testing.T, doms ...string) model {
	t.Helper()
	m := homeWithRows(t, "alpha@example.com")
	m.width, m.height = 100, 40
	m.sess.CustomerID = "cust-1"
	m.domains = doms
	m.homeFocus = homeFocusList
	(&m).openDomains()
	return m
}

func press(t *testing.T, m model, k string) (model, tea.Cmd) {
	t.Helper()
	got, cmd := m.Update(keyMsg(k))
	return got.(model), cmd
}

func sampleCheck(domain string) *services.DNSCheck {
	mx := services.DNSRecord{Type: "MX", Subdomain: "", Value: "mail.tutanota.de"}
	spf := services.DNSRecord{Type: "TXT", Subdomain: "", Value: "v=spf1 include:spf.tutanota.de -all"}
	dkim := services.DNSRecord{Type: "CNAME", Subdomain: "s1._domainkey", Value: "s1.domainkey.tutanota.de"}
	return &services.DNSCheck{
		Domain:          domain,
		CheckResult:     "OK",
		RequiredRecords: []services.DNSRecord{mx, spf, dkim},
		MissingRecords:  []services.DNSRecord{dkim},
		InvalidRecords:  []services.DNSRecord{spf},
	}
}

func TestDomains_OpensFromHomeRowWithStateAndRows(t *testing.T) {
	m := homeWithRows(t, "alpha@example.com")
	m.width, m.height = 100, 40
	m.domains = []string{"example.com", "example.org"}
	m.homeFocus = homeFocusList

	// Placeholder mode: [List aliases, Domains, Config].
	if m.navCount() != 3 || m.domainsIdx() != 1 || m.configIdx() != 2 {
		t.Fatalf("placeholder indices: nav=%d domains=%d config=%d", m.navCount(), m.domainsIdx(), m.configIdx())
	}
	if out := m.View(); !strings.Contains(out, "Domains (2)") {
		t.Errorf("the Home list has no counted Domains row:\n%s", out)
	}

	m.browseIdx = m.domainsIdx()
	got, _ := press(t, m, "enter")
	if got.cur != screenDomains {
		t.Fatalf("Enter on the Domains row went to screen %d", got.cur)
	}
	if got.domainsState == nil {
		t.Fatal("opened without state; the screen renders a placeholder")
	}
	out := got.View()
	for _, want := range []string{"Domains", "example.com", "example.org"} {
		if !strings.Contains(out, want) {
			t.Errorf("view is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(?)") || strings.Contains(out, "loading") {
		t.Errorf("view shows a placeholder:\n%s", out)
	}
	if got.subReturn != screenHome {
		t.Errorf("subReturn = %d, want Home", got.subReturn)
	}

	// Expanded list: the two trailing rows follow the alias rows.
	m.listAll = true
	if want := len(m.displayRows()) + 2; m.navCount() != want || m.domainsIdx() != want-2 || m.configIdx() != want-1 {
		t.Errorf("expanded indices: nav=%d domains=%d config=%d", m.navCount(), m.domainsIdx(), m.configIdx())
	}
	m.browseIdx = m.domainsIdx()
	if got, _ = press(t, m, "enter"); got.cur != screenDomains {
		t.Error("Enter on the Domains row of the expanded list did not open it")
	}
	m.browseIdx = m.configIdx()
	if got, _ = press(t, m, "enter"); got.cur != screenOptions {
		t.Error("the Config row stopped opening Config")
	}
}

func TestDomains_OpensFromMenuActionAndFetchesWhenEmpty(t *testing.T) {
	m := homeWithRows(t, "alpha@example.com")
	m.width, m.height = 100, 40
	m.domains = []string{"example.com"}

	got, cmd, done := m.runMenuAction("domains")
	if !done || got.cur != screenDomains || got.domainsState == nil {
		t.Fatalf("runMenuAction(domains): done=%v cur=%d state=%v", done, got.cur, got.domainsState != nil)
	}
	if cmd != nil {
		t.Error("the list was already loaded; opening must not fetch it again")
	}
	if !strings.Contains(got.View(), "example.com") {
		t.Error("rows are not rendered")
	}

	m.domains = nil
	got, cmd, _ = m.runMenuAction("domains")
	if cmd == nil || !got.domainsState.loading {
		t.Error("an empty list was not fetched on open")
	}
	if !strings.Contains(got.View(), "loading") {
		t.Error("an empty, loading list says nothing about loading")
	}
}

func TestDomains_ViewBuildsItsOwnStateWhenMissing(t *testing.T) {
	m := homeWithRows(t, "alpha@example.com")
	m.width, m.height = 100, 40
	m.domains = []string{"example.com"}
	m.cur = screenDomains
	if out := m.View(); !strings.Contains(out, "example.com") || strings.Contains(out, "(?)") {
		t.Errorf("a stateless Domains screen rendered a placeholder:\n%s", out)
	}
	if got, _ := press(t, m, "enter"); got.domainsState == nil {
		t.Error("a key on a stateless screen did not build the state")
	}
}

func TestDomains_EnterChecksOffTheUILoopAndShowsRecords(t *testing.T) {
	calls := fakeDNS(t, map[string]*services.DNSCheck{"example.com": sampleCheck("example.com")}, nil)
	m := domainsModel(t, "example.com", "example.org")

	for _, k := range []string{"enter", "^k"} {
		*calls = 0
		got, cmd := press(t, m, k)
		if cmd == nil {
			t.Fatalf("%s dispatched no command", k)
		}
		if *calls != 0 {
			t.Fatalf("%s called the API inside Update", k)
		}
		if !strings.Contains(got.flash, "checking example.com") {
			t.Errorf("%s gave no sign of working: flash=%q", k, got.flash)
		}
		if !strings.Contains(got.View(), "checking…") {
			t.Errorf("%s: the row does not say it is being checked", k)
		}

		msg := cmd()
		if *calls != 1 {
			t.Fatalf("the command made %d API calls, want 1", *calls)
		}
		next, _ := got.Update(msg)
		out := next.(model).View()
		for _, want := range []string{
			"1 invalid, 1 missing",
			"INVALID  TXT",
			"MISSING  CNAME",
			"s1._domainkey",
			"OK       MX      @",
			"mail.tutanota.de",
			"✗",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: view is missing %q:\n%s", k, want, out)
			}
		}
	}
}

func TestDomains_VerdictsUseTheDoctorVocabulary(t *testing.T) {
	ok := &services.DNSCheck{CheckResult: "OK", RequiredRecords: []services.DNSRecord{{Type: "MX", Value: "x"}}}
	missing := &services.DNSCheck{CheckResult: "OK", MissingRecords: []services.DNSRecord{{Type: "MX"}}}
	lookup := &services.DNSCheck{CheckResult: "DNS_LOOKUP_FAILED"}
	fakeDNS(t, map[string]*services.DNSCheck{"a.example": ok, "b.example": missing, "c.example": lookup}, nil)
	m := domainsModel(t, "a.example", "b.example", "c.example")
	for i, want := range []string{"✓", "!", "✗"} {
		m.domainsState.cursor = i
		got, cmd := press(t, m, "enter")
		next, _ := got.Update(cmd())
		m = next.(model)
		line := ""
		for _, l := range strings.Split(m.View(), "\n") {
			if line == "" && strings.Contains(l, m.domainsState.list[i]) {
				line = l // the row comes before the flash line that repeats the name
			}
		}
		if !strings.Contains(line, want) {
			t.Errorf("%s: line %q has no %s", m.domainsState.list[i], line, want)
		}
	}
}

func TestDomains_CheckErrorIsShownAndKeepsTheScreen(t *testing.T) {
	fakeDNS(t, nil, errors.New("boom"))
	m := domainsModel(t, "example.com")
	got, cmd := press(t, m, "enter")
	next, _ := got.Update(cmd())
	out := next.(model)
	if out.cur != screenDomains || !strings.Contains(out.errMsg, "boom") {
		t.Errorf("cur=%d errMsg=%q", out.cur, out.errMsg)
	}
	if !strings.Contains(out.View(), "✗") {
		t.Error("a failed check has no ✗ on its row")
	}
	// A second check is allowed once the first has finished.
	if _, cmd := press(t, out, "enter"); cmd == nil {
		t.Error("cannot re-check after a failure")
	}
}

func TestDomains_ACheckInFlightIsNotStartedTwice(t *testing.T) {
	fakeDNS(t, nil, nil)
	m := domainsModel(t, "example.com")
	got, _ := press(t, m, "enter")
	again, cmd := press(t, got, "enter")
	if cmd != nil || !strings.Contains(again.errMsg, "already checking") {
		t.Errorf("second Enter: cmd=%v errMsg=%q", cmd != nil, again.errMsg)
	}
}

func TestDomains_CtrlVCopiesTheOwnershipTXT(t *testing.T) {
	copied := stubClipboard(t, nil)
	m := domainsModel(t, "example.com", "example.org")
	m.domainsState.cursor = 1

	got, cmd := press(t, m, "^v")
	want := services.DomainValidationRecord("example.org", "cust-1")
	if cmd != nil {
		t.Error("copying is local and needs no command")
	}
	if *copied != want || !strings.HasPrefix(want, "t-verify=") {
		t.Errorf("copied %q, want %q", *copied, want)
	}
	if !strings.Contains(got.flash, want) || !strings.Contains(got.flash, "example.org") {
		t.Errorf("flash does not say what was copied: %q", got.flash)
	}
}

func TestDomains_CtrlVWithoutCustomerIDSaysSo(t *testing.T) {
	copied := stubClipboard(t, nil)
	m := domainsModel(t, "example.com")
	m.sess.CustomerID = ""
	got, _ := press(t, m, "^v")
	if *copied != "" {
		t.Errorf("copied %q with no customer id", *copied)
	}
	if !strings.Contains(got.errMsg, "customer_id") {
		t.Errorf("errMsg = %q", got.errMsg)
	}
}

func TestDomains_ClipboardFailureIsReported(t *testing.T) {
	stubClipboard(t, errors.New("no clipboard helper found"))
	got, _ := press(t, domainsModel(t, "example.com"), "^v")
	if !strings.Contains(got.errMsg, "clipboard") {
		t.Errorf("errMsg = %q", got.errMsg)
	}
}

func TestDomains_RefreshRefetchesAndFoldsTheListIn(t *testing.T) {
	m := domainsModel(t, "example.com")
	got, cmd := press(t, m, "^r")
	if cmd == nil || !got.domainsState.loading {
		t.Fatal("^r dispatched nothing")
	}
	next, _ := got.Update(domainsMsg{doms: []string{"example.com", "new.example"}})
	out := next.(model)
	if out.domainsState.loading || len(out.domainsState.list) != 2 || !strings.Contains(out.View(), "new.example") {
		t.Errorf("fetched list not shown: %+v", out.domainsState)
	}
	// A failed fetch keeps what is on screen.
	next, _ = out.Update(domainsMsg{err: errors.New("offline")})
	out = next.(model)
	if len(out.domainsState.list) != 2 || out.domainsState.loading {
		t.Errorf("a failed refresh disturbed the list: %+v", out.domainsState)
	}
}

func TestDomains_EscAndQReturnToWhereItWasOpened(t *testing.T) {
	for _, k := range []string{"esc", "q"} {
		for _, from := range []screen{screenHome, screenOptions} {
			m := homeWithRows(t, "alpha@example.com")
			m.width, m.height = 100, 40
			m.cur = from
			(&m).openDomains()
			got, _ := press(t, m, k)
			if got.cur != from || got.domainsState != nil {
				t.Errorf("%s from %d: cur=%d state=%v", k, from, got.cur, got.domainsState != nil)
			}
		}
	}
	// Through Help and back it still returns to the opener.
	m := domainsModel(t)
	m, _ = press(t, m, "^h")
	m, _ = press(t, m, "esc")
	if m.cur != screenDomains {
		t.Fatalf("Help did not return to Domains (cur=%d)", m.cur)
	}
	if m, _ = press(t, m, "esc"); m.cur != screenHome {
		t.Errorf("cur=%d, want Home", m.cur)
	}
}

func manyDomains(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("d%02d.example.com", i)
	}
	return out
}

func TestDomains_ScrollsAndStopsAtTheEnd(t *testing.T) {
	m := domainsModel(t, manyDomains(40)...)
	m.height = 30
	if m.domainsMaxScroll() == 0 {
		t.Fatal("40 rows fit? then this test is measuring nothing")
	}
	first := m.View()
	if strings.Contains(first, "d39.example.com") {
		t.Fatal("the last row is visible before scrolling")
	}

	for i := 0; i < 200; i++ {
		var next model
		next, _ = press(t, m, "down")
		if next.menuActive {
			m = next
			break
		}
		m = next
		if d := m.domainsState; d.cursor > 39 || d.scroll > m.domainsMaxScroll() {
			t.Fatalf("ran past the end: cursor=%d scroll=%d max=%d", d.cursor, d.scroll, m.domainsMaxScroll())
		}
	}
	d := m.domainsState
	if d.cursor != 39 || d.scroll != m.domainsMaxScroll() {
		t.Errorf("did not reach the end: cursor=%d scroll=%d max=%d", d.cursor, d.scroll, m.domainsMaxScroll())
	}
	if !m.menuActive {
		t.Error("down at the last row did not enter the bottom menu")
	}
	if out := m.View(); !strings.Contains(out, "d39.example.com") || out == first {
		t.Error("the end of the list is not shown")
	}

	// And back up: the cursor leads, up never goes below zero.
	m.menuActive = false
	for i := 0; i < 200; i++ {
		m, _ = press(t, m, "k")
	}
	if d := m.domainsState; d.cursor != 0 || d.scroll != 0 {
		t.Errorf("up did not stop at the top: cursor=%d scroll=%d", d.cursor, d.scroll)
	}
	m, _ = press(t, m, "end")
	if m.domainsState.cursor != 39 || m.domainsState.scroll != m.domainsMaxScroll() {
		t.Errorf("end: cursor=%d scroll=%d", m.domainsState.cursor, m.domainsState.scroll)
	}
	m, _ = press(t, m, "home")
	if m.domainsState.cursor != 0 || m.domainsState.scroll != 0 {
		t.Errorf("home: cursor=%d scroll=%d", m.domainsState.cursor, m.domainsState.scroll)
	}
	m, _ = press(t, m, "pgdown")
	if m.domainsState.cursor == 0 || m.domainsState.cursor > 39 {
		t.Errorf("pgdown: cursor=%d", m.domainsState.cursor)
	}
}

func TestDomains_TheCursorRowStaysOnScreen(t *testing.T) {
	m := domainsModel(t, manyDomains(40)...)
	m.height = 30
	for i := 0; i < 25; i++ {
		m, _ = press(t, m, "down")
		want := m.domainsState.list[m.domainsState.cursor]
		if !strings.Contains(m.View(), want) {
			t.Fatalf("cursor on %s but the view does not show it", want)
		}
	}
}

func TestDomains_EmptyListSaysSo(t *testing.T) {
	m := domainsModel(t)
	m.domainsState.loading = false
	if !strings.Contains(m.View(), "No custom domains") {
		t.Errorf("empty screen:\n%s", m.View())
	}
	got, cmd := press(t, m, "enter")
	if cmd != nil || got.errMsg == "" {
		t.Errorf("Enter on nothing: cmd=%v errMsg=%q", cmd != nil, got.errMsg)
	}
	if _, done := m.domainsKey("down"); done {
		t.Error("down on an empty list claims to have moved")
	}
}

// The same two guards menus_parity_test.go runs over the other screens, for
// this one: every advertised key changes something, every entry acts on Enter.
func TestDomains_EveryMenuShortcutActs(t *testing.T) {
	fakeDNS(t, nil, nil)
	stubClipboard(t, nil)
	for _, b := range domainsButtons() {
		for _, key := range strings.Split(b.key, "/") {
			t.Run(b.label+"/"+key, func(t *testing.T) {
				m := domainsModel(t, "example.com")
				before := fingerprint(m)
				got, cmd := m.Update(keyMsg(key))
				if cmd == nil && before == fingerprint(got.(model)) {
					t.Errorf("%q is advertised as %q but changed nothing", key, b.label)
				}
			})
		}
	}
}

func TestDomains_EveryMenuEntryActsOnEnter(t *testing.T) {
	fakeDNS(t, nil, nil)
	stubClipboard(t, nil)
	for i, b := range domainsButtons() {
		t.Run(b.label, func(t *testing.T) {
			m := domainsModel(t, "example.com")
			m.menuActive, m.menuSel = true, i
			before := fingerprint(m)
			got, cmd := m.Update(keyMsg("enter"))
			out := got.(model)
			if strings.Contains(out.errMsg, "not wired up") {
				t.Fatalf("the %q entry is not wired up", b.label)
			}
			if cmd == nil && before == fingerprint(out) {
				t.Errorf("Enter on %q changed nothing", b.label)
			}
		})
	}
}

func TestDomains_MenuIsInHelpAndOnTheScreen(t *testing.T) {
	m := domainsModel(t, "example.com")
	if len(m.menuFor()) != len(domainsButtons()) || m.menuFor()[0].action != "domains-check" {
		t.Error("menuFor does not return the Domains table")
	}
	var listed bool
	for _, sm := range allMenus() {
		if sm.name == "Domains" {
			listed = true
		}
	}
	if !listed {
		t.Error("Help does not list the Domains keys")
	}
	if out := m.View(); !strings.Contains(out, "Check DNS") || !strings.Contains(out, "TXT record") {
		t.Errorf("the bottom menu is not rendered:\n%s", out)
	}
}

// Help, Config, Doctor and Logs can all be opened over the Domains screen,
// and the DNS check takes a network round trip. The result arrived while one
// of them was on top and was dropped: Update only reached updateDomains when
// Domains was the current screen. The row then said "checking…" for good and
// every later check answered "already checking" with no command.
func TestDomains_CheckResultLandsWhileAnotherScreenIsOpen(t *testing.T) {
	for _, open := range []struct {
		name string
		key  string
	}{
		{"help", "^h"},
		{"config", "^o"},
	} {
		t.Run(open.name, func(t *testing.T) {
			calls := fakeDNS(t, map[string]*services.DNSCheck{"example.com": sampleCheck("example.com")}, nil)
			m := domainsModel(t, "example.com")

			got, cmd := press(t, m, "enter")
			if cmd == nil {
				t.Fatal("enter dispatched no command")
			}

			// The user looks something up while the check runs.
			got, _ = press(t, got, open.key)
			if got.cur == screenDomains {
				t.Fatalf("%s did not open anything over the Domains screen", open.key)
			}

			msg := cmd()
			if *calls != 1 {
				t.Fatalf("the command made %d API calls, want 1", *calls)
			}
			next, _ := got.Update(msg)
			got = next.(model)

			// Back on the screen the verdict is there, not "checking…".
			got, _ = press(t, got, "esc")
			if got.cur != screenDomains {
				t.Fatalf("esc did not return to the Domains screen (cur=%d)", got.cur)
			}
			if strings.Contains(got.View(), "checking…") {
				t.Errorf("the row still says it is being checked:\n%s", got.View())
			}
			if !strings.Contains(got.View(), "1 invalid, 1 missing") {
				t.Errorf("the verdict never landed:\n%s", got.View())
			}
			// And a second check can be started, which "already checking"
			// used to refuse.
			*calls = 0
			if _, cmd := press(t, got, "enter"); cmd == nil {
				t.Error("a second check dispatched nothing, so the checking flag was never cleared")
			}
		})
	}
}

// The screen's own refresh says "refreshing domains…" and nothing cleared it:
// syncDomains clears the screen's loading flag, so the line stayed under a
// list that had already been refreshed. Home's equivalent uses m.loading,
// which aliasesMsg clears.
func TestDomains_RefreshFlashIsClearedWhenTheFetchLands(t *testing.T) {
	m := domainsModel(t, "example.com")

	got, cmd := press(t, m, "^r")
	if cmd == nil {
		t.Fatal("^r dispatched no command")
	}
	if got.flash != domainsRefreshingFlash {
		t.Fatalf("flash = %q, want the refreshing line", got.flash)
	}

	next, _ := got.Update(domainsMsg{doms: []string{"example.com", "example.org"}})
	got = next.(model)

	if got.flash == domainsRefreshingFlash {
		t.Error("the flash still says it is refreshing after the list arrived")
	}
	if got.domainsState.loading {
		t.Error("the screen still says it is loading")
	}
	if len(got.domainsState.list) != 2 {
		t.Errorf("the refreshed list holds %v", got.domainsState.list)
	}
}
