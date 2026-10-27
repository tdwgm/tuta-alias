package cliflow

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/doctor"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/redact"
	"github.com/tdwgm/tuta-alias/internal/selfinstall"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// Options prints every config key with its current value + default,
// plus the session-file path/status. Mirrors the Options TUI screen.
func Options(stdout, stderr io.Writer, asJSON bool) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	values := configToMap(cfg)

	type opt struct {
		Key         string `json:"key"`
		Value       string `json:"value"`
		Default     string `json:"default"`
		Description string `json:"description"`
		Section     string `json:"section"`
	}
	out := struct {
		ConfigPath  string `json:"configPath"`
		SessionPath string `json:"sessionPath"`
		Session     string `json:"session,omitempty"`
		Options     []opt  `json:"options"`
	}{
		ConfigPath:  config.Path(),
		SessionPath: session.Path(),
	}
	if s, err := session.Load(); err == nil {
		out.Session = s.MailAddress
	}
	for _, f := range config.Fields {
		out.Options = append(out.Options, opt{
			Key:         f.Key,
			Value:       values[f.Key],
			Default:     f.Default,
			Description: f.Description,
			Section:     f.Section,
		})
	}

	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return 0
	}

	fmt.Fprintf(stdout, "Config:  %s\n", out.ConfigPath)
	if out.Session != "" {
		fmt.Fprintf(stdout, "Session: %s  (active: %s)\n", out.SessionPath, out.Session)
	} else {
		fmt.Fprintf(stdout, "Session: %s  (no session)\n", out.SessionPath)
	}
	fmt.Fprintln(stdout)

	currentSection := ""
	for _, o := range out.Options {
		if o.Section != currentSection {
			fmt.Fprintf(stdout, "[%s]\n", o.Section)
			currentSection = o.Section
		}
		def := ""
		if o.Default != "" && o.Default != o.Value {
			def = fmt.Sprintf(" (default: %q)", o.Default)
		}
		fmt.Fprintf(stdout, "  %-18s = %-30q%s\n", o.Key, o.Value, def)
	}
	return 0
}

// ConfigShow prints the raw config file contents to stdout.
func ConfigShow(stdout, stderr io.Writer) int {
	raw, err := os.ReadFile(config.Path())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	_, _ = stdout.Write(raw)
	return 0
}

// ConfigPath prints just the config-file path.
func ConfigPath(stdout io.Writer) int {
	fmt.Fprintln(stdout, config.Path())
	return 0
}

// ConfigEdit opens $EDITOR on the config file.
func ConfigEdit(stdout, stderr io.Writer) int {
	if _, err := os.Stat(config.Path()); os.IsNotExist(err) {
		// Initialise with defaults so the user has something to edit.
		_ = config.Save(config.Default())
	}
	if err := config.OpenInEditor(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}

// Install writes the config in the current format (keeping existing values),
// copies the running binary to the configured install path and drops the
// completion files the config asks for. Non-interactive; `tuta-alias install`
// without --quiet runs the TUI wizard instead.
func Install(stdout, stderr io.Writer, root *cobra.Command) int {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		if err != nil {
			fmt.Fprintf(stderr, "cannot read %s: %v\n", config.Path(), err)
			return 2
		}
		cfg = config.Default()
	}
	// Always rewrite: Save preserves comments, hand-written values and
	// unknown keys, so this upgrades an old file to the current key set
	// instead of leaving new options invisible until someone reads the
	// release notes. Previously the file was only written when missing.
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(stderr, "config write failed: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "wrote %s\n", config.Path())

	log, err := selfinstall.Apply(cfg, root)
	for _, line := range log {
		fmt.Fprintln(stdout, line)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if warn := selfinstall.PathWarning(cfg.BinaryPath()); warn != "" {
		fmt.Fprintf(stderr, "\nWarning: %s", warn)
	}
	fmt.Fprintln(stdout, "\nDone. Run `tuta-alias` to open the TUI, or `tuta-alias login` to authenticate.")
	return 0
}

// Uninstall removes the binary + completion files. With purge=true, also
// removes the config directory (which holds the saved + addresses and alias
// notes), the session, the api-token, the client-version cache and the
// keyring entries. Same implementation as the TUI's Uninstall row.
func Uninstall(stdout, stderr io.Writer, purge bool) int {
	selfinstall.Remove(stdout, purge)
	return 0
}

// ConfigGet prints the value of one config key.
func ConfigGet(stdout, stderr io.Writer, key string) int {
	key = strings.ToUpper(strings.TrimSpace(key))
	if !config.Known(key) {
		fmt.Fprintf(stderr, "unknown config key %q — see `tuta-alias options`\n", key)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintln(stdout, config.ToMap(cfg)[key])
	return 0
}

// ConfigSet validates and stores one config key, so every option is
// settable without an editor and without the TUI. The value is checked
// first: a setting that silently means something else than it says is the
// bug this guards against.
func ConfigSet(stdout, stderr io.Writer, key, value string) int {
	key = strings.ToUpper(strings.TrimSpace(key))
	if !config.Known(key) {
		fmt.Fprintf(stderr, "unknown config key %q — see `tuta-alias options`\n", key)
		return 2
	}
	if err := config.ValidateKey(key, value); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := config.Apply(cfg, key, value); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := config.Save(cfg); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintf(stdout, "%s=%q\n", key, config.ToMap(cfg)[key])
	return 0
}

// ConfigCheck validates the whole config file: bad values and keys this
// version does not know. Exit 1 when something is wrong, so it works in a
// shell check.
func ConfigCheck(stdout, stderr io.Writer) int {
	raw, err := os.ReadFile(config.Path())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(stdout, "%s does not exist yet — run `tuta-alias install`\n", config.Path())
			return 1
		}
		fmt.Fprintln(stderr, err)
		return 2
	}
	doc, err := config.Parse(raw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	problems, unknown, legacy := config.Validate(doc)
	for _, pr := range problems {
		fmt.Fprintf(stdout, "✗ %v\n", pr)
	}
	for _, k := range unknown {
		fmt.Fprintf(stdout, "? %s is not a key this version knows (kept as-is)\n", k)
	}
	for _, k := range legacy {
		fmt.Fprintf(stdout, "→ %s is the old name and is still read; the next save writes the new one\n", k)
	}
	if len(problems) == 0 {
		fmt.Fprintf(stdout, "✓ %s: %d keys, no problems\n", config.Path(), len(config.Fields))
		return 0
	}
	return 1
}

// configToMap returns all config keys (config.ToMap), so `tuta-alias options`
// never drifts from the Config struct.
func configToMap(cfg *config.Config) map[string]string { return config.ToMap(cfg) }

// Doctor prints the self-diagnostic checklist. Exit 1 when a check failed, so
// it works in a shell check; warnings do not fail it.
func Doctor(stdout, stderr io.Writer, asJSON, scrub bool) int {
	r := doctor.Run(consts.AppVersion())
	if scrub {
		for i := range r.Checks {
			r.Checks[i].Detail = redact.Text(r.Checks[i].Detail)
			r.Checks[i].Fix = redact.Text(r.Checks[i].Fix)
		}
	}
	if asJSON {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Fprintln(stdout, string(b))
		if r.Failed() {
			return 1
		}
		return 0
	}
	if scrub {
		fmt.Fprintln(stdout, redact.Notice)
	}
	fmt.Fprintf(stdout, "tuta-alias %s\n\n", r.Version)
	for _, c := range r.Checks {
		fmt.Fprintf(stdout, "  %s  %-16s %s\n", doctor.Mark(c.Level), c.Name, c.Detail)
		if c.Fix != "" && c.Level != doctor.LevelOK {
			fmt.Fprintf(stdout, "       %-16s → %s\n", "", c.Fix)
		}
	}
	fmt.Fprintln(stdout)
	switch {
	case r.Failed():
		fmt.Fprintln(stdout, "Something is wrong — see the ✗ lines above.")
		return 1
	case r.Warned():
		fmt.Fprintln(stdout, "Usable, with warnings (!).")
	default:
		fmt.Fprintln(stdout, "All good.")
	}
	return 0
}

// Logs prints the tail of the log file. The log is what the program writes
// when it decides not to interrupt you with an error.
func Logs(stdout, stderr io.Writer, lines int, showPath, clear, scrub bool) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	logx.Setup(cfg)
	if showPath {
		fmt.Fprintln(stdout, logx.Path())
		return 0
	}
	if clear {
		if err := logx.Clear(); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		fmt.Fprintf(stdout, "cleared %s\n", logx.Path())
		return 0
	}
	out, err := logx.Tail(lines)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(out) == 0 {
		fmt.Fprintf(stdout, "%s is empty (LOG_LEVEL=%q)\n", logx.Path(), cfg.LogLevel)
		return 0
	}
	if scrub {
		out = redact.Lines(out)
		fmt.Fprintln(stdout, redact.Notice)
	}
	for _, l := range out {
		fmt.Fprintln(stdout, l)
	}
	return 0
}
