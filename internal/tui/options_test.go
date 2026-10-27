package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	keyringlib "github.com/zalando/go-keyring"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/creds"
	"github.com/tdwgm/tuta-alias/internal/logx"
)

// A keyring login is always stored without expiry, so its status must not
// depend on CACHE_TTL (which only governs Bitwarden/KeePass caches).
func TestCacheStatusLine_KeyringIgnoresCacheTTL(t *testing.T) {
	for _, ttl := range []string{"8h", "reboot", "forever"} {
		t.Run(ttl, func(t *testing.T) {
			keyringlib.MockInit()
			cfg := &config.Config{CredBackend: "keyring", CacheTTL: ttl}
			if got := cacheStatusLine(cfg); !strings.Contains(got, "(empty)") {
				t.Errorf("nothing saved: status = %q, want (empty)", got)
			}
			if err := creds.SaveLogin("u@tuta.com", "pw"); err != nil {
				t.Fatal(err)
			}
			if got := cacheStatusLine(cfg); !strings.Contains(got, "no expiry") {
				t.Errorf("saved login: status = %q, want \"valid, no expiry\"", got)
			}
		})
	}
}

// Clearing the "cache" of the keyring backend deletes the saved login, and
// the flash must say so.
func TestOptionsClearCache_FlashPerBackend(t *testing.T) {
	cases := []struct {
		backend string
		want    string
	}{
		{"keyring", "saved login deleted"},
		{"bitwarden", "cache cleared"},
	}
	for _, c := range cases {
		t.Run(c.backend, func(t *testing.T) {
			keyringlib.MockInit()
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			if err := creds.SaveLogin("u@tuta.com", "pw"); err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{CredBackend: c.backend, BWItem: "tuta", CacheTTL: "8h"}
			m := model{cfg: cfg, cur: screenOptions, optsState: newOptsState(cfg)}
			m.optsIdx = -1
			for i, r := range m.optsState.rows {
				if r.action == "clear-cache" {
					m.optsIdx = i
				}
			}
			if m.optsIdx < 0 {
				t.Fatal("no clear-cache row")
			}
			got, _ := m.activateOptsRow()
			if got.flash != c.want {
				t.Errorf("flash = %q, want %q", got.flash, c.want)
			}
			_, err := creds.LoadLogin()
			if deleted := err != nil; deleted != (c.backend == "keyring") {
				t.Errorf("saved login deleted = %v (err %v)", deleted, err)
			}
		})
	}
}

// applyEdit must not write through the old *config.Config, which background
// Fetch goroutines may still be reading.
func TestApplyEdit_CopiesConfig(t *testing.T) {
	old := &config.Config{CacheTTL: "8h", BWItem: "a"}
	m := model{cfg: old}
	m.applyEdit("CACHE_TTL", "reboot")
	if old.CacheTTL != "8h" {
		t.Errorf("old config mutated: CacheTTL = %q", old.CacheTTL)
	}
	if m.cfg == old || m.cfg.CacheTTL != "reboot" || m.cfg.BWItem != "a" {
		t.Errorf("new config = %+v", m.cfg)
	}
}

// optsRowIndex returns the Options row index of a config key.
func optsRowIndex(t *testing.T, rows []optsRow, key string) int {
	t.Helper()
	for i, r := range rows {
		if r.key == key {
			return i
		}
	}
	t.Fatalf("no Options row for %s", key)
	return -1
}

// editTutaProxy types value into the TUTA_PROXY field and presses Enter.
func editTutaProxy(t *testing.T, m model, value string) model {
	t.Helper()
	m.optsIdx = optsRowIndex(t, m.optsState.rows, "TUTA_PROXY")
	m, _ = m.activateOptsRow()
	if m.optsState.state != optsEditing {
		t.Fatalf("TUTA_PROXY row did not open an editor")
	}
	m.optsState.input.SetValue(value)
	m, _ = m.updateOptions(tea.KeyMsg{Type: tea.KeyEnter})
	return m
}

// An invalid TUTA_PROXY typed in Options is refused and never saved; a valid
// one is saved and used by new clients right away.
func TestOptionsEditTutaProxy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // Options rows read the session + API token
	t.Cleanup(func() { _ = api.SetProxy("") })
	cfg := config.Default()
	m := model{cfg: cfg, cur: screenOptions, optsState: newOptsState(cfg)}

	m = editTutaProxy(t, m, "127.0.0.1:1055")
	if !strings.Contains(m.errMsg, "invalid TUTA_PROXY") {
		t.Errorf("errMsg = %q, want the validation error", m.errMsg)
	}
	if m.cfg.TutaProxy != "" {
		t.Errorf("invalid value applied: %q", m.cfg.TutaProxy)
	}
	if saved, _ := config.Load(); saved.TutaProxy != "" {
		t.Errorf("invalid value saved: %q", saved.TutaProxy)
	}

	m.errMsg = ""
	m = editTutaProxy(t, m, "socks5://127.0.0.1:1055")
	if m.errMsg != "" {
		t.Errorf("errMsg = %q", m.errMsg)
	}
	if saved, _ := config.Load(); saved.TutaProxy != "socks5://127.0.0.1:1055" {
		t.Errorf("saved TUTA_PROXY = %q", saved.TutaProxy)
	}
	if api.NewClient("https://example.invalid", "").HTTP.Transport == nil {
		t.Error("new clients still go direct after setting TUTA_PROXY")
	}
}

// A proxy URL can carry credentials, and saving one wrote the raw value to
// the log file: `tuta-alias logs` prints it, the Logs screen renders it and
// rotation copies it into .1, while uninstall --purge used to leave all of
// that behind. The line still has to name the key, or the log stops being
// useful.
func TestOptionsEditTutaProxyKeepsThePasswordOutOfTheLog(t *testing.T) {
	home := t.TempDir()
	cache := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Cleanup(func() { _ = api.SetProxy("") })

	cfg := config.Default()
	cfg.LogLevel = "info"
	logx.Setup(cfg)
	t.Cleanup(func() { logx.Setup(config.Default()) })

	m := model{cfg: cfg, cur: screenOptions, optsState: newOptsState(cfg)}
	m = editTutaProxy(t, m, "socks5://bob:s3cr3t@10.0.0.1:1055")
	if m.errMsg != "" {
		t.Fatalf("errMsg = %q, the value should have been accepted", m.errMsg)
	}

	raw, err := os.ReadFile(logx.Path())
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	got := string(raw)
	for _, secret := range []string{"s3cr3t", "bob"} {
		if strings.Contains(got, secret) {
			t.Errorf("the log file names %q:\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "TUTA_PROXY") || !strings.Contains(got, "10.0.0.1:1055") {
		t.Errorf("the log line no longer says what was saved:\n%s", got)
	}
	// The config file keeps the real value: redaction is a step on the way
	// out, never on the way in.
	if saved, _ := config.Load(); saved.TutaProxy != "socks5://bob:s3cr3t@10.0.0.1:1055" {
		t.Errorf("saved TUTA_PROXY = %q, want the value as typed", saved.TutaProxy)
	}
}

// Esc is "back one level" everywhere else, and Config is reachable from Home,
// from the Domains screen and from the login form. It went to Home from all
// three: opened over Domains it threw the screen away, and opened over the
// login form it set cur to Home while the view still drew the form, so the
// frame carried Home's bottom menu (Copy, Delete, Log out) under a form with
// no session behind it.
func TestOptionsEscGoesBackOneLevel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	t.Run("from the login form", func(t *testing.T) {
		m := model{cfg: config.Default(), cur: screenLogin}
		m.loginState = newLoginState(m.cfg)
		mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
		m = mm.(model)
		if m.cur != screenOptions {
			t.Fatalf("^o did not open Config (cur=%d)", m.cur)
		}
		mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = mm.(model)
		if m.cur != screenLogin {
			t.Errorf("cur = %d, want the login screen", m.cur)
		}
		view := m.View()
		for _, wrong := range []string{"Log out", "Delete"} {
			if strings.Contains(view, wrong) {
				t.Errorf("the login frame carries Home's menu entry %q:\n%s", wrong, view)
			}
		}
	})

	t.Run("from the Domains screen", func(t *testing.T) {
		m := domainsModel(t, "example.com")
		mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
		m = mm.(model)
		if m.cur != screenOptions {
			t.Fatalf("^o did not open Config (cur=%d)", m.cur)
		}
		mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if got := mm.(model).cur; got != screenDomains {
			t.Errorf("cur = %d, want the Domains screen", got)
		}
	})

	t.Run("from Home", func(t *testing.T) {
		m := homeWithRows(t, "a@example.com")
		m.cur = screenHome
		mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
		m = mm.(model)
		mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if got := mm.(model).cur; got != screenHome {
			t.Errorf("cur = %d, want Home", got)
		}
	})
}

// CACHE_TTL takes any Go duration in the file and from `config set`, while
// the Config screen offers five common values. A value outside that list fell
// back to index 0, so opening the chooser on a hand-written "45m" and
// pressing Enter silently replaced it with "8h": the screen destroyed a
// setting the file accepts.
func TestOptionsChoiceKeepsAValueOutsideTheList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	cfg := config.Default()
	if err := config.Apply(cfg, "CACHE_TTL", "45m"); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	m := model{cfg: cfg, cur: screenOptions, optsState: newOptsState(cfg)}
	m.optsIdx = optsRowIndex(t, m.optsState.rows, "CACHE_TTL")

	m, _ = m.activateOptsRow()
	if m.optsState.state != optsChoosing {
		t.Fatal("the CACHE_TTL row did not open a chooser")
	}
	if !contains(m.optsState.radio, "45m") {
		t.Errorf("the chooser offers %v, which does not include the current value", m.optsState.radio)
	}
	if got := m.optsState.radio[m.optsState.radioIdx]; got != "45m" {
		t.Errorf("the chooser starts on %q, want the current value 45m", got)
	}

	// Enter on the preselected entry must leave the setting alone.
	m, _ = m.updateOptions(tea.KeyMsg{Type: tea.KeyEnter})
	if m.cfg.CacheTTL != "45m" {
		t.Errorf("confirming the chooser changed CACHE_TTL to %q", m.cfg.CacheTTL)
	}
	if saved, _ := config.Load(); saved.CacheTTL != "45m" {
		t.Errorf("the file now says CACHE_TTL = %q", saved.CacheTTL)
	}
	// A value that is in the list is not duplicated.
	cfg2 := config.Default()
	m2 := model{cfg: cfg2, cur: screenOptions, optsState: newOptsState(cfg2)}
	m2.optsIdx = optsRowIndex(t, m2.optsState.rows, "CACHE_TTL")
	m2, _ = m2.activateOptsRow()
	if len(m2.optsState.radio) != len(config.CacheTTLChoices) {
		t.Errorf("the chooser offers %v for a default value, want exactly the five choices", m2.optsState.radio)
	}
}

// DEFAULT_DOMAIN is allowed to be empty: the Create-alias field then falls
// back to the first domain. The chooser offered only the account's domains,
// so once one was picked there was no way back to empty from the screen.
func TestOptionsDomainChooserCanClearTheDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	cfg := config.Default()
	cfg.DefaultDomain = "example.com"
	m := model{cfg: cfg, cur: screenOptions, optsState: newOptsState(cfg)}
	m.domains = []string{"example.com", "example.org"}
	m.optsIdx = optsRowIndex(t, m.optsState.rows, "DEFAULT_DOMAIN")

	m, _ = m.activateOptsRow()
	if m.optsState.state != optsChoosing {
		t.Fatal("the Default domain row did not open a chooser")
	}
	if m.optsState.radio[0] != "" {
		t.Errorf("the chooser offers %v, with no empty entry to clear the setting", m.optsState.radio)
	}
	if got := m.optsState.radio[m.optsState.radioIdx]; got != "example.com" {
		t.Errorf("the chooser starts on %q, want the current value", got)
	}

	// Move to the empty entry and take it.
	m, _ = m.updateOptions(tea.KeyMsg{Type: tea.KeyUp})
	m, _ = m.updateOptions(tea.KeyMsg{Type: tea.KeyEnter})
	if m.cfg.DefaultDomain != "" {
		t.Errorf("DefaultDomain = %q, want it cleared", m.cfg.DefaultDomain)
	}
	if saved, _ := config.Load(); saved.DefaultDomain != "" {
		t.Errorf("the file says DEFAULT_DOMAIN = %q", saved.DefaultDomain)
	}
}
