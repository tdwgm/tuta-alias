package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tdwgm/tuta-alias/internal/atomicfile"
)

// Config holds every user-configurable setting in the tuta-alias runtime.
// Every field maps 1:1 to a key in ~/.config/tuta-alias/tuta-alias.conf.
type Config struct {
	DefaultDomain  string
	TUIEnabled     bool
	PlusSuffixLen  string // number of random chars in auto-generated + suffixes
	ShowQuota      bool   // show alias quota under the Home banner
	ShowAliasesFor string // "List aliases" filter: a domain or a regexp (empty = all)
	CredBackend    string // "bitwarden" | "bwrun" | "keepassxc" | "keyring" | ""
	BWItem         string
	BWRunItem      string
	KeePassDB      string
	KeePassEntry   string
	CacheTTL       string // "8h" | "reboot" | "forever" | other duration
	TutaProxy      string // proxy URL for all Tuta API traffic ("" = direct)
	ServeHost      string
	ServePort      string
	CompletionBash bool
	CompletionZsh  bool
	CompletionFish bool
	// LogLevel/LogFile: off | error | info | debug, and where it goes.
	LogLevel string
	LogFile  string
	// UpdateRef/AutoUpdate/UpdateInterval: which ref to update to, whether to
	// do it on its own, and how often to look.
	UpdateRef      string
	AutoUpdate     string
	UpdateInterval string
	// InstallPath is where `install` puts the binary and where `uninstall`
	// looks for it. Empty = DefaultInstallPath(). The install wizard lets the
	// path be chosen, so uninstall has to be able to find the same one back.
	InstallPath string

	// Theme — TUI Scaffold standard v2.8. One key per element so Config →
	// Theme can colour each of them on its own, and "Reset to default"
	// restores exactly this group. Values are colour names, "accent" or
	// "default" (see internal/tui.colorFor).
	ColorAccent   string
	ColorBanner   string
	ColorHeader   string
	ColorText     string
	ColorMuted    string
	ColorShortcut string
	ColorSelected string
	ColorRule     string
	ColorRuleFoot string
	ColorIcon     string
	ColorBracket  string
	ColorSubtitle string
	ColorLive     string
	// BannerText: the logo text; empty = the app name. Anything else is drawn
	// with the font (block when the font is "default").
	BannerText string
	// BannerFont: default (the name in bold) | block | half | slant | shade.
	BannerFont string
	// LiveInfo: what the block under the banner shows — full | compact | off.
	LiveInfo string
	// MenuEffect: glitter (a glint sweeps the selected entry) | none.
	MenuEffect string
}

// Field describes one config key for mirroring across CLI/TUI/Options/file.
type Field struct {
	Key         string
	Default     string
	Description string
	Section     string
}

// Fields is the canonical field table. Adding a new option = adding a row
// here AND wiring it in Config + Load/Save + the TUI.
var Fields = []Field{
	{"DEFAULT_DOMAIN", "", "Pre-fills the Create-alias domain field.", "Defaults"},
	{"TUI_ENABLED", "true", "TUI as the no-args default (false → CLI help).", "Defaults"},
	{"PLUS_SUFFIX_LEN", "5", "Random chars in auto-generated + (sub-address) suffixes.", "Defaults"},
	{"SHOW_QUOTA", "false", "Show alias quota under the Home banner.", "Defaults"},
	{"SHOW_ALIASES_FOR", "", "'List aliases' filter: a domain or a regexp (empty = all).", "Defaults"},
	{"CRED_BACKEND", "", `Credential backend: "bitwarden" | "bwrun" | "keepassxc" | "keyring" (login saved from the TUI form) | "vault" (built-in encrypted file, for systems with no keyring) | "".`, "Credentials"},
	{"BW_ITEM", "", "Bitwarden item name or UUID (when CRED_BACKEND=bitwarden).", "Credentials"},
	{"BW_RUN_ITEM", "", "bw-run item name (when CRED_BACKEND=bwrun).", "Credentials"},
	{"KEEPASS_DB", "", "KeePassXC database path (when CRED_BACKEND=keepassxc).", "Credentials"},
	{"KEEPASS_ENTRY", "", "Entry path inside the KeePassXC database.", "Credentials"},
	{"CACHE_TTL", "8h", `Keyring cache TTL: a Go duration ("8h"), "reboot", or "forever".`, "Credentials"},
	{"TUTA_PROXY", "", `Proxy for all Tuta API traffic, e.g. "socks5://127.0.0.1:1055". With socks5:// the proxy resolves hostnames. Empty = direct. Fail-closed: when set and unreachable, requests fail — never a direct fallback.`, "Network"},
	{"SERVE_HOST", "127.0.0.1", "Bind address for tuta-alias serve.", "Serve"},
	{"SERVE_PORT", "8088", "Port for tuta-alias serve.", "Serve"},
	{"COMPLETION_BASH", "true", "Auto-install bash completion to ~/.local/share/bash-completion/.", "Shell completion"},
	{"COMPLETION_ZSH", "true", "Auto-install zsh completion to ~/.config/zsh/.", "Shell completion"},
	{"COMPLETION_FISH", "false", "Auto-install fish completion to ~/.config/fish/completions/.", "Shell completion"},

	{"INSTALL_PATH", "", "Where install puts the binary and uninstall looks for it. An absolute path (empty = ~/.local/bin/tuta-alias).", "Install"},

	{"LOG_LEVEL", "error", "What is written to the log: off | error | info | debug.", "Logging"},
	{"LOG_FILE", "", "Log file, an absolute path (empty = ~/.cache/tuta-alias/tuta-alias.log).", "Logging"},

	{"AUTO_UPDATE", "off", "When the TUI starts: off | check (say when UPDATE_REF points at a different build) | install (rebuild and replace the binary).", "Update"},
	{"UPDATE_REF", "main", "The branch or tag an update comes from.", "Update"},
	{"UPDATE_INTERVAL", "24h", "How often to look for a newer build (a Go duration).", "Update"},

	{"THEME", "blue", `Theme group: "Reset theme" (Config, ^t) restores every key from here down. Accent: blue | ocean | green | red | yellow | magenta | cyan | orange | purple. The other colours also take: accent | default | grey | light grey | white`, themeSection},
	{"COLOR_BANNER", "accent", "", themeSection},
	{"COLOR_HEADER", "accent", "", themeSection},
	{"COLOR_TEXT", "white", "", themeSection},
	{"COLOR_MUTED", "accent", "", themeSection},
	{"COLOR_SHORTCUT", "accent", "Menu shortcuts and separators.", themeSection},
	{"COLOR_SELECTED", "white", "The selected menu entry.", themeSection},
	{"COLOR_RULE", "grey", "The dotted line under the banner.", themeSection},
	{"COLOR_RULE_FOOTER", "grey", "The dotted line above the bottom menu.", themeSection},
	{"COLOR_ICON", "accent", "Markers: the ▶ cursor on a row, and ✓ ! ✗ on the Doctor screen.", themeSection},
	{"COLOR_BRACKET", "grey", "The [ ] around an action, the ( ) of a choice list, and the › on a row that opens a page.", themeSection},
	{"COLOR_SUBTITLE", "white", "Version, description and byline under the logo.", themeSection},
	{"COLOR_LIVE", "blue", "The live info block under the rule.", themeSection},
	{"BANNER_TEXT", "", `Banner text: empty = "tuta-alias"; anything else is drawn with the font.`, themeSection},
	{"BANNER_FONT", "logo", "Banner font: logo (the tool's own drawn logo) | block | half | slant | shade.", themeSection},
	{"LIVE_INFO", "full", "Live info under the banner: full (account, aliases, quota) | compact | off.", themeSection},
	{"MENU_EFFECT", "glitter", "Selected menu entry: glitter (a glint sweeps the text) | none.", themeSection},
}

// legacyKeys maps a config key's former name to its current one. The TUI
// Scaffold standard calls the accent key THEME (so does the reference
// implementation in tdwgm/go-tui); this project shipped it as COLOR_ACCENT.
var legacyKeys = map[string]string{"COLOR_ACCENT": "THEME"}

// themeSection is the Config screen group that "Reset theme" restores. The
// name is what the Config screen prints as its section header, so it is a
// plain word; what the group means is said in COLOR_ACCENT's description.
const themeSection = "Theme"

// ShortPath prints $HOME as "~" so a path fits a narrow terminal and a
// screenshot does not leak the account name.
//
// The separator check matters: a plain prefix test turns /home/username/x
// into "~name/x" for a user whose home is /home/user, which is both wrong and
// a small leak of the real path.
func ShortPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	home = strings.TrimRight(home, string(os.PathSeparator))
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

// ThemeSection is the section name of the theme group, so the Config screen
// can put it on its own page without hardcoding the string.
func ThemeSection() string { return themeSection }

// Path returns the hardcoded config-file path. No XDG_CONFIG_HOME override.
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "tuta-alias", "tuta-alias.conf")
}

// DefaultInstallPath is where the binary goes when INSTALL_PATH is empty.
func DefaultInstallPath() string { return inHome(".local", "bin", "tuta-alias") }

// BashCompletionPath, ZshCompletionPath and FishCompletionPath are the
// per-user completion files `install` writes and `uninstall` removes. They
// live here, next to Path(), because install and uninstall used to keep their
// own copies of the list: the TUI removed three paths and the CLI removed
// three others, and nothing made them agree.
func BashCompletionPath() string {
	return inHome(".local", "share", "bash-completion", "completions", "tuta-alias")
}

// ZshCompletionPath is the zsh completion file.
func ZshCompletionPath() string { return inHome(".config", "zsh", "_tuta-alias") }

// FishCompletionPath is the fish completion file.
func FishCompletionPath() string {
	return inHome(".config", "fish", "completions", "tuta-alias.fish")
}

func inHome(parts ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(append([]string{home}, parts...)...)
}

// BinaryPath returns the install path in force: INSTALL_PATH when set,
// otherwise DefaultInstallPath(). install and uninstall must agree on this,
// or uninstall leaves the binary behind.
func (c *Config) BinaryPath() string {
	// Absolute only. `config set` refuses anything else, but the file is
	// edited by hand: INSTALL_PATH="~/bin/tuta-alias" (no shell to expand the
	// tilde) had install write the binary to $PWD/~/bin/tuta-alias while the
	// report named the path as typed, and uninstall run from another
	// directory looked for it somewhere else again. config check and doctor
	// report the value as invalid; this is what keeps it from being acted on
	// in the meantime.
	if p := strings.TrimSpace(c.InstallPath); filepath.IsAbs(p) {
		return p
	}
	return DefaultInstallPath()
}

// Default returns a Config populated with the defaults declared in Fields.
func Default() *Config {
	c := &Config{}
	fromMap(c, map[string]string{}, true)
	return c
}

// Load reads the config file and returns the resulting Config. If the file
// does not exist, Default() is returned.
func Load() (*Config, error) {
	p := Path()
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	doc, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, f := range Fields {
		if v, ok := doc.Get(f.Key); ok {
			values[f.Key] = v
		}
	}
	// Keys this version renamed: read the old name when the new one is
	// absent, so an existing config file keeps its value.
	for old, now := range legacyKeys {
		if _, ok := values[now]; ok {
			continue
		}
		if v, ok := doc.Get(old); ok {
			values[now] = v
		}
	}
	c := &Config{}
	fromMap(c, values, true)
	return c, nil
}

// Save writes cfg to the config file, creating the directory if needed.
// Existing comments and unknown keys are preserved.
func Save(cfg *Config) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(p), err)
	}

	var doc *Doc
	if raw, err := os.ReadFile(p); err == nil {
		doc, _ = Parse(raw)
	}
	if doc == nil || len(doc.entries) == 0 {
		doc = template()
	}

	m := toMap(cfg)
	// Belt and braces for the same injection ValidateKey refuses: a caller
	// that skipped validation must not be able to corrupt the file either.
	for k, v := range m {
		if strings.ContainsAny(v, "\n\r\x00") {
			return fmt.Errorf("refusing to write %s: the value contains a control character", k)
		}
	}
	for _, f := range Fields {
		doc.Set(f.Key, m[f.Key])
	}
	// Drop the pre-rename names: Save keeps unknown keys on purpose, and an
	// ignored COLOR_ACCENT line next to a live THEME line is a trap.
	for old := range legacyKeys {
		doc.Delete(old)
	}

	return atomicfile.Write(p, doc.Bytes(), 0o600)
}

// Editor returns the editor to open the config file with: $EDITOR, else
// $VISUAL, else vim. The TUI's "Edit config in $EDITOR" row and
// `tuta-alias config edit` both read it here, so they cannot pick differently.
func Editor() string {
	if e := os.Getenv("EDITOR"); e != "" {
		return e
	}
	if e := os.Getenv("VISUAL"); e != "" {
		return e
	}
	return "vim"
}

// OpenInEditor runs Editor() on the config file. Caller should reload config
// after this returns.
func OpenInEditor() error {
	editor := Editor()
	bin, err := exec.LookPath(editor)
	if err != nil {
		return fmt.Errorf("editor %q not found in PATH", editor)
	}
	cmd := exec.Command(bin, Path())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func fromMap(cfg *Config, values map[string]string, useDefaults bool) {
	raw := func(key string) string {
		if v, ok := values[key]; ok {
			return v
		}
		if useDefaults {
			for _, f := range Fields {
				if f.Key == key {
					return f.Default
				}
			}
		}
		return ""
	}
	// Canonical here, which is every path into the struct: the file, `config
	// set`, the Config screen and the install wizard. ValidateKey accepts a
	// choice in any case, so CRED_BACKEND="BitWarden" was a valid setting
	// that no consumer matched, and the backend was silently inactive.
	pick := func(key string) string { return Canonical(key, raw(key)) }
	parseBool := func(s string) bool {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "1", "yes":
			return true
		}
		return false
	}
	cfg.DefaultDomain = pick("DEFAULT_DOMAIN")
	cfg.TUIEnabled = parseBool(pick("TUI_ENABLED"))
	cfg.PlusSuffixLen = pick("PLUS_SUFFIX_LEN")
	cfg.ShowQuota = parseBool(pick("SHOW_QUOTA"))
	cfg.ShowAliasesFor = pick("SHOW_ALIASES_FOR")
	cfg.CredBackend = pick("CRED_BACKEND")
	cfg.BWItem = pick("BW_ITEM")
	cfg.BWRunItem = pick("BW_RUN_ITEM")
	cfg.KeePassDB = pick("KEEPASS_DB")
	cfg.KeePassEntry = pick("KEEPASS_ENTRY")
	cfg.CacheTTL = pick("CACHE_TTL")
	cfg.TutaProxy = pick("TUTA_PROXY")
	cfg.ServeHost = pick("SERVE_HOST")
	cfg.ServePort = pick("SERVE_PORT")
	cfg.CompletionBash = parseBool(pick("COMPLETION_BASH"))
	cfg.CompletionZsh = parseBool(pick("COMPLETION_ZSH"))
	cfg.CompletionFish = parseBool(pick("COMPLETION_FISH"))
	cfg.InstallPath = strings.TrimSpace(pick("INSTALL_PATH"))
	cfg.LogLevel = oneOf(pick("LOG_LEVEL"), "error", LogLevels)
	cfg.LogFile = strings.TrimSpace(pick("LOG_FILE"))
	cfg.AutoUpdate = oneOf(pick("AUTO_UPDATE"), "off", AutoUpdateModes)
	cfg.UpdateRef = strings.TrimSpace(pick("UPDATE_REF"))
	if cfg.UpdateRef == "" {
		cfg.UpdateRef = "main"
	}
	cfg.UpdateInterval = strings.TrimSpace(pick("UPDATE_INTERVAL"))

	// Theme. oneOf keeps an unknown value from poisoning the whole theme:
	// the field falls back to its own default instead, so a hand-edited conf
	// with one typo still renders.
	cfg.ColorAccent = oneOf(pick("THEME"), "blue", AccentNames)
	cfg.ColorBanner = oneOf(pick("COLOR_BANNER"), "accent", ColorNames)
	cfg.ColorHeader = oneOf(pick("COLOR_HEADER"), "accent", ColorNames)
	cfg.ColorText = oneOf(pick("COLOR_TEXT"), "white", ColorNames)
	cfg.ColorMuted = oneOf(pick("COLOR_MUTED"), "accent", ColorNames)
	cfg.ColorShortcut = oneOf(pick("COLOR_SHORTCUT"), "accent", ColorNames)
	cfg.ColorSelected = oneOf(pick("COLOR_SELECTED"), "white", ColorNames)
	cfg.ColorRule = oneOf(pick("COLOR_RULE"), "grey", ColorNames)
	cfg.ColorRuleFoot = oneOf(pick("COLOR_RULE_FOOTER"), "grey", ColorNames)
	cfg.ColorIcon = oneOf(pick("COLOR_ICON"), "accent", ColorNames)
	cfg.ColorBracket = oneOf(pick("COLOR_BRACKET"), "grey", ColorNames)
	cfg.ColorSubtitle = oneOf(pick("COLOR_SUBTITLE"), "white", ColorNames)
	cfg.ColorLive = oneOf(pick("COLOR_LIVE"), "blue", ColorNames)
	cfg.BannerText = pick("BANNER_TEXT")
	// "logo" is the standard's name for the tool's own drawn logo. Configs
	// written by 0.11.x say "default", which named an indirection rather than
	// what it draws, so it is still accepted on read and normalised here.
	if font := strings.ToLower(strings.TrimSpace(pick("BANNER_FONT"))); font == "default" {
		cfg.BannerFont = "logo"
	} else {
		cfg.BannerFont = oneOf(font, "logo", BannerFonts)
	}
	cfg.LiveInfo = oneOf(pick("LIVE_INFO"), "full", LiveInfoModes)
	cfg.MenuEffect = oneOf(pick("MENU_EFFECT"), "glitter", MenuEffects)
}

// ColorNames, BannerFonts, LiveInfoModes and MenuEffects are the accepted
// values for the theme keys. They live here rather than in internal/tui so the
// config file, the Config screen and the parser all read the same list.
var (
	ColorNames = []string{
		"accent", "default", "blue", "ocean", "green", "red", "yellow",
		"magenta", "cyan", "orange", "purple", "grey", "light grey", "white",
	}
	// AccentNames is COLOR_ACCENT's own choice list. "accent" and "default"
	// are meaningless for the key the others follow, and grey/white/light grey
	// as an accent make the focus marker invisible against the body text —
	// applyStyle rejects them, so the Config screen must not offer them.
	AccentNames = []string{"blue", "ocean", "green", "red", "yellow", "magenta", "cyan", "orange", "purple"}
	BannerFonts = []string{"logo", "block", "half", "slant", "shade"}
	// LogLevels are the LOG_LEVEL values, least to most verbose. internal/logx
	// reads the same list.
	LogLevels = []string{"off", "error", "info", "debug"}
	// AutoUpdateModes are the AUTO_UPDATE values. "check" only tells you;
	// "install" replaces the binary without asking, which is why it is not
	// the default.
	AutoUpdateModes = []string{"off", "check", "install"}
	LiveInfoModes   = []string{"full", "compact", "off"}
	MenuEffects     = []string{"glitter", "none"}
)

// oneOf returns v when it is one of allowed, otherwise def.
func oneOf(v, def string, allowed []string) string {
	s := strings.ToLower(strings.TrimSpace(v))
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	return def
}

// ToMap returns every config key with its current value (conf-file form).
func ToMap(cfg *Config) map[string]string { return toMap(cfg) }

func toMap(cfg *Config) map[string]string {
	b := func(v bool) string {
		if v {
			return "true"
		}
		return "false"
	}
	return map[string]string{
		"DEFAULT_DOMAIN":   cfg.DefaultDomain,
		"TUI_ENABLED":      b(cfg.TUIEnabled),
		"PLUS_SUFFIX_LEN":  cfg.PlusSuffixLen,
		"SHOW_QUOTA":       b(cfg.ShowQuota),
		"SHOW_ALIASES_FOR": cfg.ShowAliasesFor,
		"CRED_BACKEND":     cfg.CredBackend,
		"BW_ITEM":          cfg.BWItem,
		"BW_RUN_ITEM":      cfg.BWRunItem,
		"KEEPASS_DB":       cfg.KeePassDB,
		"KEEPASS_ENTRY":    cfg.KeePassEntry,
		"CACHE_TTL":        cfg.CacheTTL,
		"TUTA_PROXY":       cfg.TutaProxy,
		"SERVE_HOST":       cfg.ServeHost,
		"SERVE_PORT":       cfg.ServePort,
		"COMPLETION_BASH":  b(cfg.CompletionBash),
		"COMPLETION_ZSH":   b(cfg.CompletionZsh),
		"COMPLETION_FISH":  b(cfg.CompletionFish),
		"INSTALL_PATH":     cfg.InstallPath,
		"LOG_LEVEL":        cfg.LogLevel,
		"LOG_FILE":         cfg.LogFile,
		"AUTO_UPDATE":      cfg.AutoUpdate,
		"UPDATE_REF":       cfg.UpdateRef,
		"UPDATE_INTERVAL":  cfg.UpdateInterval,

		"THEME":             cfg.ColorAccent,
		"COLOR_BANNER":      cfg.ColorBanner,
		"COLOR_HEADER":      cfg.ColorHeader,
		"COLOR_TEXT":        cfg.ColorText,
		"COLOR_MUTED":       cfg.ColorMuted,
		"COLOR_SHORTCUT":    cfg.ColorShortcut,
		"COLOR_SELECTED":    cfg.ColorSelected,
		"COLOR_RULE":        cfg.ColorRule,
		"COLOR_RULE_FOOTER": cfg.ColorRuleFoot,
		"COLOR_ICON":        cfg.ColorIcon,
		"COLOR_BRACKET":     cfg.ColorBracket,
		"COLOR_SUBTITLE":    cfg.ColorSubtitle,
		"COLOR_LIVE":        cfg.ColorLive,
		"BANNER_TEXT":       cfg.BannerText,
		"BANNER_FONT":       cfg.BannerFont,
		"LIVE_INFO":         cfg.LiveInfo,
		"MENU_EFFECT":       cfg.MenuEffect,
	}
}

// Apply sets one key on cfg by its config-file name.
//
// It goes through the same map the file is written from, so there is exactly
// one place that knows how a key maps to a struct field. The Config screen
// used to carry its own switch over all 30 keys (a second copy), and the
// install wizard a third one built out of struct literals, which is how
// TUTA_PROXY and the whole Theme group ended up being droppable.
func Apply(cfg *Config, key, value string) error {
	if !Known(key) {
		return fmt.Errorf("unknown config key %q", key)
	}
	m := toMap(cfg)
	m[key] = value
	fromMap(cfg, m, true)
	return nil
}

// ApplyAll sets several keys at once (the install wizard's commit).
func ApplyAll(cfg *Config, values map[string]string) error {
	m := toMap(cfg)
	for k, v := range values {
		if !Known(k) {
			return fmt.Errorf("unknown config key %q", k)
		}
		m[k] = v
	}
	fromMap(cfg, m, true)
	return nil
}

// ResetTheme restores every key in the Theme section to its default, and
// nothing else — this is what Config → "Reset to default" calls.
func ResetTheme(cfg *Config) {
	defaults := map[string]string{}
	for _, f := range Fields {
		if f.Section == themeSection {
			defaults[f.Key] = f.Default
		}
	}
	merged := toMap(cfg)
	for k, v := range defaults {
		merged[k] = v
	}
	fromMap(cfg, merged, true)
}

// template returns a fresh Doc with section headers + each known key at
// its default value. Used the first time Save() runs against a missing file.
func template() *Doc {
	d := NewDoc()
	d.AppendComment("tuta-alias configuration. Edit by hand or via Options in the TUI.")
	d.AppendComment("Format: shell-style KEY=\"value\" with comments starting with #.")
	d.AppendBlank()

	var lastSection string
	for _, f := range Fields {
		if f.Section != lastSection {
			d.AppendBlank()
			d.AppendComment(strings.Repeat("─", 60))
			d.AppendComment(f.Section)
			lastSection = f.Section
		}
		d.AppendComment(f.Description)
		d.Set(f.Key, f.Default)
	}
	return d
}

// PortInt parses ServePort as an int. Returns 0 on parse failure.
func (c *Config) PortInt() int {
	n, err := strconv.Atoi(c.ServePort)
	if err != nil {
		return 0
	}
	return n
}

// PlusSuffixLenInt parses PlusSuffixLen as an int, falling back to 5 on
// any parse failure and flooring the result to at least 1.
func (c *Config) PlusSuffixLenInt() int {
	n, err := strconv.Atoi(strings.TrimSpace(c.PlusSuffixLen))
	if err != nil || n < 1 {
		return 5
	}
	return n
}
