// Package doctor is the self-diagnostic checklist: what is installed, what is
// configured, and what the program would do with it right now.
//
// Everything here is offline and side-effect free. It reads files and looks
// for programs on $PATH; it never talks to Tuta, never writes anything, and
// never touches the system keyring — a diagnostic that pops an unlock dialog
// or creates the file it is reporting on is not a diagnostic.
package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/notes"
	"github.com/tdwgm/tuta-alias/internal/plusalias"
	"github.com/tdwgm/tuta-alias/internal/selfinstall"
	"github.com/tdwgm/tuta-alias/internal/serve"
	"github.com/tdwgm/tuta-alias/internal/session"
	"github.com/tdwgm/tuta-alias/internal/update"
	"github.com/tdwgm/tuta-alias/internal/vault"
)

// Level is how much a check's result matters.
type Level string

const (
	LevelOK   Level = "ok"   // as it should be
	LevelWarn Level = "warn" // works, but worth knowing
	LevelFail Level = "fail" // something is actually wrong
)

// Check is one line of the checklist.
type Check struct {
	Name   string `json:"name"`
	Level  Level  `json:"level"`
	Detail string `json:"detail"`
	// Fix is what to run or change, when there is an obvious next step.
	Fix string `json:"fix,omitempty"`
}

// Report is the whole checklist.
type Report struct {
	Version string  `json:"version"`
	Checks  []Check `json:"checks"`
}

// Failed reports whether any check failed (warnings do not count).
func (r Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Level == LevelFail {
			return true
		}
	}
	return false
}

// Warned reports whether any check warned.
func (r Report) Warned() bool {
	for _, c := range r.Checks {
		if c.Level == LevelWarn {
			return true
		}
	}
	return false
}

// Run collects the checklist.
func Run(version string) Report {
	r := Report{Version: version}
	add := func(name string, level Level, detail, fix string) {
		r.Checks = append(r.Checks, Check{Name: name, Level: level, Detail: detail, Fix: fix})
	}

	// --- config file ------------------------------------------------------
	cfg := config.Default()
	raw, err := os.ReadFile(config.Path())
	switch {
	case os.IsNotExist(err):
		add("config", LevelWarn, Short(config.Path())+" does not exist (defaults in use)", "tuta-alias install --quiet")
	case err != nil:
		add("config", LevelFail, err.Error(), "")
	default:
		doc, perr := config.Parse(raw)
		if perr != nil {
			add("config", LevelFail, perr.Error(), "")
			break
		}
		problems, unknown, legacy := config.Validate(doc)
		if loaded, lerr := config.Load(); lerr == nil && loaded != nil {
			cfg = loaded
		}
		switch {
		case len(problems) > 0:
			msgs := make([]string, 0, len(problems))
			for _, p := range problems {
				msgs = append(msgs, p.Error())
			}
			add("config", LevelFail, strings.Join(msgs, "; "), "tuta-alias config check")
		case len(unknown) > 0:
			add("config", LevelWarn, Short(config.Path())+" has keys this version does not know: "+strings.Join(unknown, ", "), "")
		case len(legacy) > 0:
			// Not a warning: the key is read and the next save rewrites it.
			add("config", LevelOK, fmt.Sprintf("%s (%d keys, old name still in the file: %s)",
				Short(config.Path()), len(config.Fields), strings.Join(legacy, ", ")), "")
		default:
			add("config", LevelOK, fmt.Sprintf("%s (%d keys)", Short(config.Path()), len(config.Fields)), "")
		}
	}

	// --- installed binary -------------------------------------------------
	bin := cfg.BinaryPath()
	if _, err := os.Stat(bin); err != nil {
		add("installed", LevelWarn, Short(bin)+" is not installed (running from somewhere else)", "tuta-alias install")
	} else {
		add("installed", LevelOK, Short(bin), "")
	}
	dir := filepath.Dir(bin)
	if !inPath(dir) {
		// The absolute path, not Short(dir): a ~ inside double quotes does
		// not expand, so the suggested line would silently add a directory
		// called "~". selfinstall builds the same line for `install`, and it
		// is the one place that knows what a shell still does inside those
		// quotes: a directory with a $ or a backquote in its name ran as code.
		add("PATH", LevelWarn, Short(dir)+" is not in $PATH", selfinstall.ExportPathLine(dir))
	} else if other, err := exec.LookPath("tuta-alias"); err == nil && other != bin {
		add("PATH", LevelWarn, "another tuta-alias comes first: "+Short(other), "")
	} else {
		add("PATH", LevelOK, Short(dir), "")
	}

	// --- shell completion ---------------------------------------------------
	for _, c := range []struct {
		name string
		on   bool
		path string
	}{
		{"completion bash", cfg.CompletionBash, config.BashCompletionPath()},
		{"completion zsh", cfg.CompletionZsh, config.ZshCompletionPath()},
		{"completion fish", cfg.CompletionFish, config.FishCompletionPath()},
	} {
		if !c.on {
			continue
		}
		if _, err := os.Stat(c.path); err != nil {
			add(c.name, LevelWarn, "enabled but "+Short(c.path)+" is missing", "tuta-alias install --quiet")
		} else {
			add(c.name, LevelOK, Short(c.path), "")
		}
	}

	// --- session ------------------------------------------------------------
	if s, err := session.Load(); err != nil {
		add("session", LevelWarn, "no session — not logged in", "tuta-alias login")
	} else {
		detail := s.MailAddress
		if age := sessionAge(s.SavedAt); age != "" {
			detail += "  (saved " + age + " ago)"
		}
		add("session", LevelOK, detail, "")
	}

	// --- credential backend --------------------------------------------------
	switch cfg.CredBackend {
	case "":
		add("credentials", LevelOK, "no backend — the password is typed each time", "")
	case "bitwarden":
		add("credentials", toolLevel("bw"), backendDetail("bitwarden", "bw", cfg.BWItem), toolFix("bw"))
	case "bwrun":
		add("credentials", toolLevel("bw-run"), backendDetail("bwrun", "bw-run", cfg.BWRunItem), toolFix("bw-run"))
	case "keepassxc":
		add("credentials", toolLevel("keepassxc-cli"),
			backendDetail("keepassxc", "keepassxc-cli", cfg.KeePassDB+" / "+cfg.KeePassEntry), toolFix("keepassxc-cli"))
	case "keyring":
		add("credentials", LevelOK, "system keyring (not read here — it can prompt)", "")
	case "vault":
		if vault.Exists() {
			add("credentials", LevelOK, "encrypted vault: "+Short(vault.Path())+" (not opened here)", "")
		} else {
			add("credentials", LevelFail, "CRED_BACKEND is \"vault\" but there is no vault file", "tuta-alias vault set")
		}
	default:
		add("credentials", LevelFail, "unknown CRED_BACKEND "+cfg.CredBackend, "tuta-alias config check")
	}
	if cfg.CredBackend == "keepassxc" {
		if cfg.KeePassDB == "" || cfg.KeePassEntry == "" {
			add("keepass", LevelFail, "KEEPASS_DB and KEEPASS_ENTRY must both be set", "tuta-alias config set KEEPASS_DB ...")
		} else if _, err := os.Stat(cfg.KeePassDB); err != nil {
			add("keepass", LevelFail, "database not found: "+Short(cfg.KeePassDB), "")
		}
	}

	// --- network ------------------------------------------------------------
	if cfg.TutaProxy == "" {
		add("proxy", LevelOK, "direct (TUTA_PROXY empty)", "")
	} else if u, err := api.ParseProxy(cfg.TutaProxy); err != nil {
		add("proxy", LevelFail, err.Error(), "tuta-alias config set TUTA_PROXY ...")
	} else {
		add("proxy", LevelOK, u.Redacted()+" (fail-closed; reachability not tested here)", "")
	}

	// --- serve --------------------------------------------------------------
	serveDetail := fmt.Sprintf("%s:%s", cfg.ServeHost, cfg.ServePort)
	if !serve.IsLoopback(cfg.ServeHost) {
		add("serve", LevelWarn, serveDetail+" is reachable beyond this machine", "tuta-alias config set SERVE_HOST 127.0.0.1")
	} else {
		add("serve", LevelOK, serveDetail, "")
	}
	switch tok, err := serve.PeekToken(); {
	case err == nil && tok != "":
		// The token is a bearer token for the local API, so a mode that lets
		// another account on this machine read it is worth saying. serve makes
		// the file private when it starts; this only looks, because a
		// checklist that fixes what it reports is not one.
		if mode := serve.LooseTokenMode(); mode != 0 {
			add("api token", LevelWarn,
				fmt.Sprintf("%s is open to other users (mode %04o); serve makes it private when it starts",
					Short(serve.TokenPath()), mode),
				"chmod 600 "+serve.TokenPath())
		} else {
			add("api token", LevelOK, Short(serve.TokenPath()), "")
		}
	case err != nil && !os.IsNotExist(err):
		// A file that is there and cannot be read is not "not created yet":
		// serve would fail to start and this said everything was fine.
		add("api token", LevelWarn, Short(serve.TokenPath())+": "+err.Error(), "")
	default:
		add("api token", LevelOK, "not created yet (serve makes one)", "")
	}

	// --- local stores ---------------------------------------------------------
	if entries, err := plusalias.Load(); err != nil {
		add("+ addresses", LevelFail, err.Error(), "")
	} else {
		add("+ addresses", LevelOK, fmt.Sprintf("%d saved  (%s)", len(entries), Short(plusalias.Path())), "")
	}
	if n, err := notes.Load(); err != nil {
		add("alias notes", LevelFail, err.Error(), "")
	} else {
		add("alias notes", LevelOK, fmt.Sprintf("%d saved  (%s)", len(n), Short(notes.Path())), "")
	}

	// --- update -----------------------------------------------------------------
	switch {
	case cfg.AutoUpdate == "off":
		add("update", LevelOK, fmt.Sprintf("manual (ref %s, AUTO_UPDATE=\"off\")", cfg.UpdateRef), "")
	default:
		add("update", LevelOK, fmt.Sprintf("%s from %s every %s", cfg.AutoUpdate, cfg.UpdateRef, cfg.UpdateInterval), "")
	}
	if err := update.Available(); err != nil {
		// Not a failure of the install: it only means `tuta-alias update`
		// cannot run here, which is worth knowing before you need it.
		add("update tooling", LevelWarn, err.Error(), "")
	} else if consts.CommitHash() == "" {
		add("update tooling", LevelWarn,
			"this binary records no commit, so an update cannot tell whether it is current",
			"rebuild it inside the git checkout, or reinstall with `tuta-alias update`")
	} else if consts.Modified() {
		add("update tooling", LevelWarn,
			"built from "+consts.CommitHash()+" with uncommitted changes, so an update cannot tell whether it is current",
			"commit or stash, then rebuild")
	} else {
		add("update tooling", LevelOK, "git and go are available; built from "+consts.CommitHash(), "")
	}

	// --- log ------------------------------------------------------------------
	// Read where the log WOULD be, without calling logx.Setup: a diagnostic
	// that reconfigures the process it is diagnosing is not one.
	logPath := logx.PathFor(cfg.LogFile)
	switch size := logx.SizeOf(logPath); {
	case cfg.LogLevel == "off":
		add("log", LevelOK, "off (LOG_LEVEL=\"off\")", `tuta-alias config set LOG_LEVEL error`)
	case size < 0:
		add("log", LevelOK, fmt.Sprintf("%s — nothing written yet (level %s)", Short(logPath), cfg.LogLevel), "")
	default:
		add("log", LevelOK, fmt.Sprintf("%s — %s, level %s", Short(logPath), humanBytes(size), cfg.LogLevel), "")
	}

	// --- tools the TUI uses ----------------------------------------------------
	if tool := firstOnPath("wl-copy", "pbcopy", "xclip"); tool != "" {
		add("clipboard", LevelOK, tool, "")
	} else {
		add("clipboard", LevelWarn, "no wl-copy, pbcopy or xclip — copying will fail", "")
	}
	editor := config.Editor()
	if p, err := exec.LookPath(editor); err == nil {
		add("editor", LevelOK, editor+" ("+p+")", "")
	} else {
		add("editor", LevelWarn, editor+" is not on $PATH", "export EDITOR=vim")
	}

	// --- leftovers from the old name --------------------------------------------
	if old := legacyLeftovers(); len(old) > 0 {
		add("legacy", LevelWarn, "state from the tuta-cli days is still here: "+strings.Join(old, ", "), "")
	}
	return r
}

func backendDetail(name, tool, item string) string {
	item = strings.TrimSpace(strings.Trim(item, "/ "))
	if item == "" {
		item = "(item not set)"
	}
	if _, err := exec.LookPath(tool); err != nil {
		return name + ": " + tool + " is not on $PATH"
	}
	return name + ": " + item
}

func toolLevel(tool string) Level {
	if _, err := exec.LookPath(tool); err != nil {
		return LevelFail
	}
	return LevelOK
}

func toolFix(tool string) string {
	if _, err := exec.LookPath(tool); err != nil {
		return "install " + tool
	}
	return ""
}

func firstOnPath(names ...string) string {
	for _, n := range names {
		if _, err := exec.LookPath(n); err == nil {
			return n
		}
	}
	return ""
}

func inPath(dir string) bool {
	abs, _ := filepath.Abs(dir)
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir || p == abs {
			return true
		}
	}
	return false
}

func sessionAge(savedAt string) string {
	t, err := time.Parse(time.RFC3339, savedAt)
	if err != nil {
		return ""
	}
	d := time.Since(t).Truncate(time.Minute)
	if d < 0 {
		return ""
	}
	return d.String()
}

// legacyLeftovers lists pre-0.10 state directories that still exist.
func legacyLeftovers() []string {
	var out []string
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	for _, p := range []string{
		filepath.Join(home, ".config", "tuta-cli"),
		filepath.Join(home, ".cache", "tuta-cli"),
	} {
		if _, err := os.Stat(p); err == nil {
			out = append(out, Short(p))
		}
	}
	return out
}

// humanBytes renders a file size for one line of a checklist.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// Short prints $HOME as "~". One implementation, in internal/config, so the
// checklist and the TUI shorten a path the same way.
func Short(p string) string { return config.ShortPath(p) }

// Mark is the ✓ / ! / ✗ for a level.
func Mark(l Level) string {
	switch l {
	case LevelFail:
		return "✗"
	case LevelWarn:
		return "!"
	}
	return "✓"
}
