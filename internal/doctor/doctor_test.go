package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/serve"
)

// isolate gives the test its own HOME and cache dir. The logger is reset
// afterwards as well: Run must not touch it (it reads where the log would be
// rather than calling logx.Setup), and this is what would catch it starting
// to again.
func isolate(t *testing.T) (home, cache string) {
	t.Helper()
	home, cache = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Cleanup(func() { logx.Setup(nil) })
	return home, cache
}

// writeConfig saves Default() with the given key/value pairs applied, through
// the same code the installer uses. Apply does not validate, so a deliberately
// bad value goes through it too.
func writeConfig(t *testing.T, kv ...string) {
	t.Helper()
	cfg := config.Default()
	for i := 0; i+1 < len(kv); i += 2 {
		if err := config.Apply(cfg, kv[i], kv[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
}

func find(t *testing.T, r Report, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("the report has no %q check; it has %v", name, names(r))
	return Check{}
}

func names(r Report) []string {
	var out []string
	for _, c := range r.Checks {
		out = append(out, c.Name)
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// A diagnostic that writes the file it reports on is not a diagnostic: the
// first `tuta-alias doctor` on a fresh machine would create a config, an
// api-token or a log, and the next run would report them as if they had
// always been there.
func TestRunOnEmptyHomeFailsNothingAndCreatesNothing(t *testing.T) {
	home, cache := isolate(t)

	r := Run("test-version")

	if r.Failed() {
		for _, c := range r.Checks {
			if c.Level == LevelFail {
				t.Errorf("fresh machine reported a failure: %s: %s", c.Name, c.Detail)
			}
		}
	}
	if r.Version != "test-version" {
		t.Errorf("Report.Version = %q, expected the version passed to Run", r.Version)
	}
	for name, p := range map[string]string{
		"config file": config.Path(),
		"api-token":   serve.TokenPath(),
		"log file":    logx.Path(),
	} {
		if exists(p) {
			t.Errorf("Run created the %s at %s", name, p)
		}
	}
	for _, d := range []string{home, cache} {
		entries, err := os.ReadDir(d)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("Run left %d entries in %s, expected it to be untouched", len(entries), d)
		}
	}
	if c := find(t, r, "config"); c.Level != LevelWarn {
		t.Errorf("a missing config file is %s, expected warn (defaults still work)", c.Level)
	}
}

// Same promise when a config exists: reading it must not rewrite it (which
// would drop the user's comments) and the log it points at must not appear
// just because doctor looked.
func TestRunDoesNotRewriteExistingConfigOrStartALog(t *testing.T) {
	isolate(t)
	writeConfig(t, "LOG_LEVEL", "debug")
	before, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}

	Run("v")

	after, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("Run modified the config file it was only meant to read")
	}
	if exists(logx.Path()) {
		t.Error("Run created the log file at LOG_LEVEL=debug just by looking at it")
	}
	if exists(serve.TokenPath()) {
		t.Error("Run created an api-token")
	}
}

// `doctor` exits non-zero on a failed config, so a value the validator
// rejects must be a fail, and a clean file must not cry wolf.
func TestConfigCheckLevel(t *testing.T) {
	for _, tc := range []struct {
		name string
		kv   []string
		want Level
	}{
		{"defaults are ok", nil, LevelOK},
		{"port out of range fails", []string{"SERVE_PORT", "70000"}, LevelFail},
		{"non-numeric port fails", []string{"SERVE_PORT", "http"}, LevelFail},
		{"suffix length zero fails", []string{"PLUS_SUFFIX_LEN", "0"}, LevelFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			writeConfig(t, tc.kv...)

			c := find(t, Run("v"), "config")
			if c.Level != tc.want {
				t.Errorf("config check is %s (%s), expected %s", c.Level, c.Detail, tc.want)
			}
			if tc.want == LevelFail && !strings.Contains(c.Detail, tc.kv[0]) {
				t.Errorf("the failure does not name the offending key %s: %q", tc.kv[0], c.Detail)
			}
		})
	}
}

// A key from a newer (or older) version must not fail the run: the user can
// downgrade and keep their file. It is worth a warning so a typo shows up.
func TestUnknownConfigKeyWarnsInsteadOfFailing(t *testing.T) {
	isolate(t)
	writeConfig(t)
	f, err := os.OpenFile(config.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("FROM_THE_FUTURE=\"1\"\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	r := Run("v")
	c := find(t, r, "config")
	if c.Level != LevelWarn {
		t.Errorf("config with an unknown key is %s (%s), expected warn", c.Level, c.Detail)
	}
	if !strings.Contains(c.Detail, "FROM_THE_FUTURE") {
		t.Errorf("the warning does not name the unknown key: %q", c.Detail)
	}
	if r.Failed() {
		t.Error("an unknown key alone made the whole report fail")
	}
}

// Anything but loopback exposes the API token-protected server to the LAN;
// the warning is the only place the user is told.
func TestServeHostCheck(t *testing.T) {
	for _, tc := range []struct {
		host string
		want Level
	}{
		{"127.0.0.1", LevelOK},
		{"localhost", LevelOK},
		{"::1", LevelOK},
		{"0.0.0.0", LevelWarn},
		{"192.168.1.20", LevelWarn},
		{"::", LevelWarn},
	} {
		t.Run(tc.host, func(t *testing.T) {
			isolate(t)
			writeConfig(t, "SERVE_HOST", tc.host)

			c := find(t, Run("v"), "serve")
			if c.Level != tc.want {
				t.Errorf("SERVE_HOST=%s gives serve check %s (%s), expected %s", tc.host, c.Level, c.Detail, tc.want)
			}
			if !strings.Contains(c.Detail, tc.host) {
				t.Errorf("the serve line does not show the host %s: %q", tc.host, c.Detail)
			}
		})
	}
}

// TUTA_PROXY is fail-closed, so a malformed one means every request will
// fail; the doctor must say so before the user finds out from a timeout.
func TestProxyCheck(t *testing.T) {
	for _, tc := range []struct {
		name, proxy string
		want        Level
		detailHas   string
	}{
		{"empty is direct", "", LevelOK, "direct"},
		{"socks5 proxy", "socks5://127.0.0.1:1055", LevelOK, "127.0.0.1:1055"},
		{"no scheme", "127.0.0.1:1055", LevelFail, ""},
		{"unsupported scheme", "ftp://127.0.0.1:21", LevelFail, ""},
		{"no port", "socks5://127.0.0.1", LevelFail, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			writeConfig(t, "TUTA_PROXY", tc.proxy)

			c := find(t, Run("v"), "proxy")
			if c.Level != tc.want {
				t.Errorf("TUTA_PROXY=%q gives proxy check %s (%s), expected %s", tc.proxy, c.Level, c.Detail, tc.want)
			}
			if !strings.Contains(c.Detail, tc.detailHas) {
				t.Errorf("proxy detail %q should mention %q", c.Detail, tc.detailHas)
			}
		})
	}
}

// The report is pasted into bug reports. A proxy password in it would be
// published with them.
func TestProxyPasswordNeverAppearsInReport(t *testing.T) {
	for _, proxy := range []string{
		"socks5://alice:hunter2@127.0.0.1:1055",
		"ftp://alice:hunter2@127.0.0.1:21",
	} {
		isolate(t)
		writeConfig(t, "TUTA_PROXY", proxy)

		for _, c := range Run("v").Checks {
			if strings.Contains(c.Detail, "hunter2") || strings.Contains(c.Fix, "hunter2") {
				t.Errorf("TUTA_PROXY=%q: the %s check leaks the password: %q", proxy, c.Name, c.Detail)
			}
		}
	}
}

// A KeePassXC database that is not there fails at the first login; the
// "keepass" line is what tells the user which path is wrong.
func TestKeepassMissingDatabaseFails(t *testing.T) {
	home, _ := isolate(t)
	missing := filepath.Join(home, "vault", "nope.kdbx")
	writeConfig(t, "CRED_BACKEND", "keepassxc", "KEEPASS_DB", missing, "KEEPASS_ENTRY", "tuta/main")

	r := Run("v")
	c := find(t, r, "keepass")
	if c.Level != LevelFail {
		t.Errorf("keepass check is %s (%s) for a database that does not exist, expected fail", c.Level, c.Detail)
	}
	if !strings.Contains(c.Detail, "nope.kdbx") {
		t.Errorf("the failure does not name the missing database: %q", c.Detail)
	}
	if !r.Failed() {
		t.Error("report.Failed() is false although the keepass database is missing")
	}
}

func TestKeepassNeedsDatabaseAndEntry(t *testing.T) {
	isolate(t)
	writeConfig(t, "CRED_BACKEND", "keepassxc")

	if c := find(t, Run("v"), "keepass"); c.Level != LevelFail {
		t.Errorf("keepass check is %s (%s) with no KEEPASS_DB or KEEPASS_ENTRY, expected fail", c.Level, c.Detail)
	}
}

// Backends that need no external tool must not fail on a machine without
// one, and an unknown backend name must, because login will refuse it. The
// keyring backend is only named, never opened: reading it can prompt.
func TestCredentialBackendCheck(t *testing.T) {
	for _, tc := range []struct {
		backend string
		want    Level
	}{
		{"", LevelOK},
		{"keyring", LevelOK},
		{"lastpass", LevelFail},
	} {
		t.Run("backend="+tc.backend, func(t *testing.T) {
			isolate(t)
			writeConfig(t, "CRED_BACKEND", tc.backend)

			c := find(t, Run("v"), "credentials")
			if c.Level != tc.want {
				t.Errorf("CRED_BACKEND=%q gives credentials check %s (%s), expected %s", tc.backend, c.Level, c.Detail, tc.want)
			}
		})
	}
}

// The exit code of `tuta-alias doctor` hangs on these two: a warning must not
// fail the run, and a failure must not be hidden by surrounding ok lines.
func TestFailedAndWarned(t *testing.T) {
	ok := Check{Name: "a", Level: LevelOK, Detail: "x"}
	warn := Check{Name: "b", Level: LevelWarn, Detail: "x"}
	fail := Check{Name: "c", Level: LevelFail, Detail: "x"}

	for _, tc := range []struct {
		name           string
		checks         []Check
		failed, warned bool
	}{
		{"empty", nil, false, false},
		{"only ok", []Check{ok, ok}, false, false},
		{"warn only", []Check{ok, warn}, false, true},
		{"fail only", []Check{ok, fail}, true, false},
		{"both", []Check{warn, ok, fail}, true, true},
		{"fail is last", []Check{ok, ok, fail}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Report{Checks: tc.checks}
			if r.Failed() != tc.failed {
				t.Errorf("Failed() = %v for %q, expected %v", r.Failed(), tc.name, tc.failed)
			}
			if r.Warned() != tc.warned {
				t.Errorf("Warned() = %v for %q, expected %v", r.Warned(), tc.name, tc.warned)
			}
		})
	}
}

// A checklist line with nothing on it is useless, and in the JSON output an
// empty name or detail looks like a bug in the script reading it. Checked
// across scenarios because most of the lines only exist in some of them.
func TestEveryCheckHasNameAndDetail(t *testing.T) {
	for _, tc := range []struct {
		name string
		kv   []string
		cfg  bool
	}{
		{"empty home", nil, false},
		{"defaults", nil, true},
		{"bad values", []string{"SERVE_PORT", "70000", "TUTA_PROXY", "nonsense", "SERVE_HOST", "0.0.0.0"}, true},
		{"keepassxc", []string{"CRED_BACKEND", "keepassxc", "KEEPASS_DB", "/nonexistent/db.kdbx", "KEEPASS_ENTRY", "e"}, true},
		{"shell completions on", []string{"COMPLETION_BASH", "true", "COMPLETION_ZSH", "true", "COMPLETION_FISH", "true"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			if tc.cfg {
				writeConfig(t, tc.kv...)
			}
			r := Run("v")
			if len(r.Checks) == 0 {
				t.Fatal("Run returned an empty checklist")
			}
			for i, c := range r.Checks {
				if strings.TrimSpace(c.Name) == "" {
					t.Errorf("check %d has no name (detail %q)", i, c.Detail)
				}
				if strings.TrimSpace(c.Detail) == "" {
					t.Errorf("check %q has no detail", c.Name)
				}
				if c.Level != LevelOK && c.Level != LevelWarn && c.Level != LevelFail {
					t.Errorf("check %q has level %q, which is not ok, warn or fail", c.Name, c.Level)
				}
			}
		})
	}
}

// Short exists so a pasted report does not carry the account name.
func TestShortHidesHome(t *testing.T) {
	home, _ := isolate(t)
	for _, tc := range []struct{ in, want string }{
		{filepath.Join(home, ".config", "x"), "~/.config/x"},
		{home, "~"},
		{"/etc/hosts", "/etc/hosts"},
	} {
		if got := Short(tc.in); got != tc.want {
			t.Errorf("Short(%q) = %q, expected %q", tc.in, got, tc.want)
		}
	}
}

func TestMark(t *testing.T) {
	seen := map[string]Level{}
	for _, l := range []Level{LevelOK, LevelWarn, LevelFail} {
		m := Mark(l)
		if m == "" {
			t.Errorf("Mark(%s) is empty", l)
		}
		if prev, dup := seen[m]; dup {
			t.Errorf("Mark(%s) and Mark(%s) are both %q; levels must be told apart at a glance", l, prev, m)
		}
		seen[m] = l
	}
}

// The api token is a bearer token for the local REST API, so a mode that lets
// another account on this machine read it is a finding, not an OK line. The
// check used to be LevelOK whatever the mode was. serve makes the file private
// when it starts; the doctor only looks, because a checklist that fixes what
// it reports is not one.
func TestApiTokenModeIsReported(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode os.FileMode
		want Level
	}{
		{"private", 0o600, LevelOK},
		{"group readable", 0o640, LevelWarn},
		{"world readable", 0o644, LevelWarn},
		{"world writable", 0o666, LevelWarn},
		{"owner read only", 0o400, LevelOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			if _, err := serve.GetOrCreateToken(); err != nil {
				t.Fatal(err)
			}
			p := serve.TokenPath()
			if err := os.Chmod(p, tc.mode); err != nil {
				t.Fatal(err)
			}

			c := find(t, Run("v"), "api token")
			if c.Level != tc.want {
				t.Errorf("mode %04o gives the api token check %s (%s), expected %s", tc.mode, c.Level, c.Detail, tc.want)
			}
			if tc.want == LevelWarn {
				if !strings.Contains(c.Detail, "open to other users") {
					t.Errorf("the line does not say what is wrong: %q", c.Detail)
				}
				if !strings.Contains(c.Fix, "chmod 600") {
					t.Errorf("the line does not say how to fix it: %q", c.Fix)
				}
			}
			// Whatever it reported, it must not have changed the file: that is
			// serve's job, and a diagnostic that acts cannot be run twice for
			// the same answer.
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := fi.Mode().Perm(); got != tc.mode {
				t.Errorf("the doctor changed the mode from %04o to %04o", tc.mode, got)
			}
		})
	}
}

// The PATH advice is a line the user pastes into a shell. It comes from
// selfinstall, which is the one place that knows what a shell still
// interprets inside double quotes: this used to build its own unescaped copy,
// so an install directory with a $ or a backquote in its name expanded to
// something else or ran as code.
func TestPathAdviceIsSafeToPasteIntoAShell(t *testing.T) {
	home, _ := isolate(t)
	// A directory name a shell would act on: $HOME expands to somewhere else,
	// and the backquotes run a command, which leaves the marker file behind if
	// the line is not escaped.
	marker := filepath.Join(home, "ran")
	dir := filepath.Join(home, "bin $HOME `touch '"+marker+"'`")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, "INSTALL_PATH", filepath.Join(dir, "tuta-alias"))

	c := find(t, Run("v"), "PATH")
	if c.Level != LevelWarn {
		t.Fatalf("PATH check is %s (%s), expected a warning for a directory not in $PATH", c.Level, c.Detail)
	}

	// Reading the line is not the test: `\$HOME` contains the text `$HOME`, so
	// a substring check passes on the escaped line and on the raw one alike.
	// The only honest check is to run it, which is what the user does.
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh to paste the line into: %v", err)
	}
	out, err := exec.Command(sh, "-c", "PATH=/nonexistent; "+c.Fix+`; printf '%s' "$PATH"`).CombinedOutput()
	if err != nil {
		t.Fatalf("the suggested line did not run: %v\n%s", err, out)
	}
	if exists(marker) {
		t.Error("pasting the line ran a command out of the directory name")
	}
	if want := dir + ":/nonexistent"; string(out) != want {
		t.Errorf("the line set PATH to %q, want %q: the directory has to survive the escaping byte for byte", out, want)
	}
}
