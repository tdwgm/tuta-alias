// style.go — the theme: palette, per-element colours and the shared styles
// (TUI Scaffold standard v2.8; the same theme as gu-vpn-pid and the Go
// template, wired to this project's own internal/config).
//
// Body text follows COLOR_TEXT (white by default), secondary text is muted,
// section headers are ocean blue, the accent marks focus (▶) and titles,
// errors are red. Nothing is hard-coded.
//
// Every element is a config key (Config → Theme), so one part can be
// recoloured without touching the rest:
//
//	THEME           the accent: focus marker, titles, keys
//	COLOR_BANNER    the block logo                      (default: accent)
//	COLOR_HEADER    section headers, ✓ messages         (default: ocean)
//	COLOR_TEXT      body text and values                (default: white)
//	COLOR_MUTED     info labels, hints                  (default: grey)
//	COLOR_SHORTCUT  menu shortcuts and separators       (default: grey)
//	COLOR_SELECTED  the selected menu entry             (default: white)
//	COLOR_RULE      the dotted rule under the banner    (default: grey)
//	COLOR_RULE_FOOTER the rule above the bottom menu     (default: grey)
//	COLOR_ICON      the ▶ cursor and the ✓ ! ✗ marks    (default: accent)
//	COLOR_BRACKET   [ ] around actions, ( ) in a list   (default: grey)
//	COLOR_SUBTITLE  version, description, byline        (default: white)
//	COLOR_LIVE      the live info block                 (default: blue)
//
// Each takes a colour name from config.ColorNames; "accent" follows THEME
// (the accent key, called COLOR_ACCENT before 0.12) and "default" means no
// colour at all.
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/tdwgm/tuta-alias/internal/config"
)

// Fixed palette. DangerColor/OKColor are semantic and also offered as theme
// colours ("red", "green").
var (
	MutedColor  = lipgloss.Color("#888888")
	DangerColor = lipgloss.Color("#FF7A7A")
	OKColor     = lipgloss.Color("#88CC88")
	BgColor     = lipgloss.Color("#232323")
)

var namedColors = map[string]lipgloss.Color{
	"blue":       "#ACC7FF", // Tuta blue
	"ocean":      "#7AA2F7", // deeper blue: the default section headers
	"green":      OKColor,
	"red":        DangerColor,
	"yellow":     "#F3F99D",
	"magenta":    "#FF79C6",
	"cyan":       "#8BE9FD",
	"orange":     "#FFB86C",
	"purple":     "#BD93F9",
	"grey":       MutedColor,
	"light grey": "#BBBBBB",
	"white":      "#EEEEEE",
}

// Live theme colours (rebuilt by applyStyle).
var (
	AccentColor lipgloss.Color         = namedColors["blue"]
	BannerColor lipgloss.TerminalColor = namedColors["blue"]
	HeaderColor lipgloss.TerminalColor = namedColors["ocean"]
	TextColor   lipgloss.TerminalColor = namedColors["white"]
	HintColor   lipgloss.TerminalColor = MutedColor
)

// Element styles (rebuilt by applyStyle) and the banner/live choices.
var (
	shortcutStyle   = lipgloss.NewStyle().Foreground(MutedColor)
	selectedStyle   = lipgloss.NewStyle()
	subtitleStyle   = lipgloss.NewStyle().Foreground(MutedColor)
	liveStyle       = lipgloss.NewStyle()
	bylineStyle     = lipgloss.NewStyle().Foreground(MutedColor)
	bannerRuleStyle = lipgloss.NewStyle().Foreground(MutedColor).Faint(true)
	footerRuleStyle = lipgloss.NewStyle().Foreground(MutedColor).Faint(true)
	// legalStyle is the fine print under the bottom menu: always grey and
	// faint, whatever the theme does, because that is what fine print is.
	legalStyle = lipgloss.NewStyle().Foreground(MutedColor).Faint(true)
	// iconStyle is every marker the screens draw (▶ ✓ ! ✗); bracketStyle is
	// the punctuation around a choice or an action.
	iconStyle    = lipgloss.NewStyle()
	bracketStyle = lipgloss.NewStyle().Foreground(MutedColor)

	uiBannerFont = "logo"
	uiBannerText = ""
	uiLiveInfo   = "full"
)

// Shared styles, rebuilt by applyStyle.
var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(AccentColor)
	keyStyle    = lipgloss.NewStyle().Bold(true).Foreground(AccentColor)
	bannerStyle = lipgloss.NewStyle().Bold(true).Foreground(BannerColor)
	okStyle     = lipgloss.NewStyle().Foreground(HeaderColor)
	fgStyle     = lipgloss.NewStyle().Foreground(TextColor)
	mutedStyle  = lipgloss.NewStyle().Foreground(HintColor)
	errStyle    = lipgloss.NewStyle().Foreground(DangerColor)
	// warnStyle is the in-progress colour; it follows the accent (no orange).
	warnStyle = lipgloss.NewStyle().Foreground(AccentColor)
)

// colorFor resolves an element colour name. Unknown names fall back to def.
func colorFor(name string, def lipgloss.TerminalColor) lipgloss.TerminalColor {
	switch name {
	case "accent":
		return AccentColor
	case "default":
		return lipgloss.NoColor{}
	}
	if c, ok := namedColors[name]; ok {
		return c
	}
	return def
}

// applyStyle rebuilds every colour and style from cfg. Called at start, after
// a config save/reload, and on every theme change (live preview).
func applyStyle(cfg *config.Config) {
	if cfg == nil {
		return
	}
	// The accent is always a real colour — "accent" and "default" are only
	// meaningful for the elements that follow it, and grey/white as an accent
	// would make focus invisible against the body text.
	// grey/white/light grey as an accent make the focus marker invisible
	// against the body text, so they are not offered (config.AccentNames) and
	// are refused here too for a hand-edited file.
	if c, ok := namedColors[cfg.ColorAccent]; ok && !strings.Contains(cfg.ColorAccent, "grey") && cfg.ColorAccent != "white" {
		AccentColor = c
	} else {
		AccentColor = namedColors["blue"]
	}
	BannerColor = colorFor(cfg.ColorBanner, AccentColor)
	HeaderColor = colorFor(cfg.ColorHeader, AccentColor)
	TextColor = colorFor(cfg.ColorText, namedColors["white"])
	HintColor = colorFor(cfg.ColorMuted, AccentColor)

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(AccentColor)
	keyStyle = lipgloss.NewStyle().Bold(true).Foreground(AccentColor)
	warnStyle = lipgloss.NewStyle().Foreground(AccentColor)
	bannerStyle = lipgloss.NewStyle().Bold(true).Foreground(BannerColor)
	okStyle = lipgloss.NewStyle().Foreground(HeaderColor)
	fgStyle = lipgloss.NewStyle().Foreground(TextColor)
	mutedStyle = lipgloss.NewStyle().Foreground(HintColor)
	shortcutStyle = lipgloss.NewStyle().Foreground(colorFor(cfg.ColorShortcut, AccentColor))
	selectedStyle = lipgloss.NewStyle().Foreground(colorFor(cfg.ColorSelected, namedColors["white"]))
	subtitleStyle = lipgloss.NewStyle().Foreground(colorFor(cfg.ColorSubtitle, namedColors["white"]))
	liveStyle = lipgloss.NewStyle().Foreground(colorFor(cfg.ColorLive, namedColors["blue"]))
	bylineStyle = subtitleStyle
	legalStyle = lipgloss.NewStyle().Foreground(colorFor("grey", MutedColor)).Faint(true)
	bannerRuleStyle = lipgloss.NewStyle().Foreground(colorFor(cfg.ColorRule, MutedColor)).Faint(true)
	// The reference implementation draws a second rule above the bottom menu
	// (go-tui app.go:435) and colours it with its own key, so this project
	// does too: a key that exists in the standard and paints nothing here is
	// what the audit was about.
	footerRuleStyle = lipgloss.NewStyle().Foreground(colorFor(cfg.ColorRuleFoot, MutedColor)).Faint(true)
	iconStyle = lipgloss.NewStyle().Bold(true).Foreground(colorFor(cfg.ColorIcon, AccentColor))
	bracketStyle = lipgloss.NewStyle().Foreground(colorFor(cfg.ColorBracket, MutedColor))

	uiBannerFont, uiLiveInfo, uiBannerText = cfg.BannerFont, cfg.LiveInfo, cfg.BannerText
	uiMenuEffect = cfg.MenuEffect
}

// txt renders body text in COLOR_TEXT. It exists because the screens used to
// write plain strings straight into the output: the theme key was there but
// painted nothing, so labels and alias addresses took the terminal's own
// foreground instead of the colour the user picked. Anything that is body
// text — a label, a value, an address — goes through here.
func txt(s string) string { return fgStyle.Render(s) }
