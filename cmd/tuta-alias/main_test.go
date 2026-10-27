package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/cliflow"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/tui"
)

func TestRoot_HelpListsFlatCommands(t *testing.T) {
	buf := &bytes.Buffer{}
	cmd := buildRoot()
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--help"})
	_ = cmd.Execute()
	out := buf.String()
	for _, c := range []string{"login", "logout", "whoami", "domains", "aliases", "create", "delete", "quota", "serve"} {
		if !bytes.Contains([]byte(out), []byte(c)) {
			t.Errorf("--help missing command %q\nout=%s", c, out)
		}
	}
	if bytes.Contains([]byte(out), []byte(" alias ")) {
		t.Errorf("`alias` subgroup should be removed — found in --help: %s", out)
	}
}

// cobra sorts Commands(), so "the last added command" is not install.
func TestInstall_OwnsQuietFlag(t *testing.T) {
	root := buildRoot()
	for _, c := range root.Commands() {
		has := c.Flags().Lookup("quiet") != nil
		if has != (c.Name() == "install") {
			t.Errorf("%s: has --quiet = %v", c.Name(), has)
		}
	}
}

func TestRoot_NoArgs_RunsTUI(t *testing.T) {
	cmd := buildRoot()
	if cmd.Run == nil {
		t.Errorf("rootCmd.Run must be set so `tuta-alias` with no args runs TUI")
	}
}

// writeConf writes a tuta-alias.conf under a temp HOME.
func writeConf(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := filepath.Join(home, ".config", "tuta-alias")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tuta-alias.conf"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// An invalid TUTA_PROXY stops a network command before it starts, with a
// one-line error; offline commands (the way to fix it) still run.
func TestInvalidTutaProxy_BlocksNetworkCommandsOnly(t *testing.T) {
	writeConf(t, `TUTA_PROXY="socks4://127.0.0.1:1055"`+"\n")
	t.Cleanup(func() { _ = api.SetProxy("") })

	for _, args := range [][]string{{"aliases", "--json"}, {"login", "--auto"}, {"domains"}} {
		out, errb := &bytes.Buffer{}, &bytes.Buffer{}
		cmd := buildRoot()
		cmd.SetOut(out)
		cmd.SetErr(errb)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("%v: ran with an invalid TUTA_PROXY", args)
		}
		msg := errb.String()
		if !strings.Contains(msg, "invalid TUTA_PROXY") || !strings.Contains(msg, "config edit") ||
			strings.Count(strings.TrimSpace(msg), "\n") != 0 {
			t.Errorf("%v: stderr = %q, want one line naming TUTA_PROXY and the fix", args, msg)
		}
	}

	// Commands with a Run that calls os.Exit cannot be Executed in a test, so
	// check the hook on them directly.
	root := buildRoot()
	for _, path := range [][]string{{"config", "edit"}, {"config", "show"}, {"options"}, {"plus", "list"}, {"version"}} {
		c, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("%v: %v", path, err)
		}
		if err := applyProxy(c, nil); err != nil {
			t.Errorf("%v blocked by an invalid TUTA_PROXY: %v", path, err)
		}
	}
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil || !strings.Contains(out.String(), "tuta-alias ") {
		t.Errorf("version: err=%v out=%q", err, out.String())
	}
}

// A valid TUTA_PROXY is applied before any command runs.
func TestValidTutaProxy_AppliedAtStartup(t *testing.T) {
	writeConf(t, `TUTA_PROXY="socks5://127.0.0.1:1055"`+"\n")
	t.Cleanup(func() { _ = api.SetProxy("") })
	cmd := buildRoot()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if api.NewClient("https://example.invalid", "").HTTP.Transport == nil {
		t.Error("TUTA_PROXY from the config file was not applied")
	}
}

// Help's command list is generated from cobra, so it cannot go stale. It had:
// the hand-written list in the TUI was missing logout, whoami, dns-check,
// verify-record, plus, options, config and version — eight of seventeen.
func TestTUIHelpListsEveryCommand(t *testing.T) {
	root := buildRoot()
	listed := map[string]bool{}
	for _, r := range tui.CommandRows(root) {
		listed[r[0]] = true
		if r[1] == "" {
			t.Errorf("command %q has no one-line description for Help", r[0])
		}
	}
	for _, c := range root.Commands() {
		if c.Hidden || !c.IsAvailableCommand() {
			continue
		}
		if !listed[c.Name()] {
			t.Errorf("the TUI Help screen does not list the %q command", c.Name())
		}
	}
	if len(listed) < 10 {
		t.Errorf("only %d commands listed; the command tree is not being read", len(listed))
	}
}

// Every config key must be settable from the CLI, with no editor and no TUI.
func TestConfigSetCoversEveryKey(t *testing.T) {
	writeConf(t, "")
	for _, f := range config.Fields {
		value := f.Default
		if c := config.Choices(f.Key); c != nil {
			value = c[len(c)-1]
		}
		if value == "" {
			continue
		}
		out, errb := &bytes.Buffer{}, &bytes.Buffer{}
		if code := cliflow.ConfigSet(out, errb, f.Key, value); code != 0 {
			t.Errorf("config set %s %q: exit %d (%s)", f.Key, value, code, errb.String())
			continue
		}
		got, errb2 := &bytes.Buffer{}, &bytes.Buffer{}
		if code := cliflow.ConfigGet(got, errb2, f.Key); code != 0 {
			t.Errorf("config get %s: exit %d (%s)", f.Key, code, errb2.String())
			continue
		}
		if strings.TrimSpace(got.String()) != value {
			t.Errorf("config get %s = %q, want %q", f.Key, strings.TrimSpace(got.String()), value)
		}
	}
}

// A value the file would reinterpret must be refused by `config set`, and
// `config check` must find one that is already in the file.
func TestConfigSetAndCheckRefuseBadValues(t *testing.T) {
	writeConf(t, "")
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	if code := cliflow.ConfigSet(out, errb, "SERVE_PORT", "70000"); code == 0 {
		t.Error("config set accepted SERVE_PORT=70000")
	}
	if !strings.Contains(errb.String(), "port number") {
		t.Errorf("stderr = %q, want the reason", errb.String())
	}

	writeConf(t, "SERVE_PORT=\"70000\"\nNOT_A_KEY=\"x\"\n")
	out, errb = &bytes.Buffer{}, &bytes.Buffer{}
	if code := cliflow.ConfigCheck(out, errb); code != 1 {
		t.Errorf("config check exit = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "SERVE_PORT") || !strings.Contains(out.String(), "NOT_A_KEY") {
		t.Errorf("config check output = %q", out.String())
	}
}

// `serve` names the token file in its banner, on stdout and in the non-loopback
// warning. With no HOME and no absolute XDG_CACHE_HOME there is nowhere to keep
// it, so both lines printed "Token file: " with nothing after it, then
// "Listening on …" as though it had started, and the reason came last. The
// banner is only printed once there is a token.
//
// It runs in a child process because the command calls os.Exit, which an
// in-process test cannot survive. The child is this test binary, which is the
// standard way to reach an exiting path.
func TestServe_WithNowhereToKeepTheTokenSaysSoAndNamesNothing(t *testing.T) {
	const childEnv = "TUTA_ALIAS_TEST_SERVE_CHILD"
	if os.Getenv(childEnv) == "1" {
		cmd := buildRoot()
		cmd.SetArgs([]string{"serve"})
		_ = cmd.Execute()
		return
	}

	child := exec.Command(os.Args[0], "-test.run="+t.Name(), "-test.v=false")
	// Not t.Setenv: that is for this process, and the point is what the child
	// sees. HOME and XDG_CACHE_HOME are dropped, not emptied, because an empty
	// one is a different case (a relative path, which is also refused).
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "XDG_CACHE_HOME=") {
			continue
		}
		child.Env = append(child.Env, e)
	}
	child.Env = append(child.Env, childEnv+"=1")
	out, err := child.CombinedOutput()
	if err == nil {
		t.Fatalf("serve exited 0 with nowhere to keep its token:\n%s", out)
	}
	s := string(out)

	if strings.Contains(s, "Listening on") {
		t.Errorf("it said it was listening when it never got that far:\n%s", s)
	}
	if strings.Contains(s, "Token file: \n") || strings.Contains(s, "Token file: Listening") {
		t.Errorf("the banner named the token file as the empty string:\n%s", s)
	}
	if !strings.Contains(s, "nowhere to keep the API token") {
		t.Errorf("it did not say why it stopped:\n%s", s)
	}
}
