// banner.go — the banner, TUI Scaffold standard v2.8 (same shape as the Go
// template and gu-vpn-pid): the block logo with `v<version>` directly after
// its last line, the one-line description, the Fraktur byline, and the ONE
// dotted rule. In the TUI a live info block follows directly under that rule.
//
// Text is indented; the rule starts at column 0.
package tui

import (
	"strings"

	"github.com/tdwgm/tuta-alias/internal/consts"
)

// App metadata. AppName is what the screen titles read (`tuta-alias › Home`)
// and the default banner text.
var (
	AppName        = "tuta-alias"
	AppDescription = "Tuta Mail alias manager"
	// Version follows the compiled-in version so there is one source.
	Version    = consts.AppVersion()
	BinaryName = "tuta-alias"
	// LegalNotice is the fine print at the very bottom of every screen and at
	// the end of `tuta-alias version`. tuta-alias is an independent tool that
	// speaks Tuta's API; saying so where it is always visible, rather than
	// only in the README, is the point.
	LegalNotice = "Independent, unofficial tool. Not affiliated with, endorsed by or supported by Tutao GmbH. " +
		"Tuta® and Tutanota® are registered trademarks of Tutao GmbH."
	// BannerArt is the box-drawing logo, 3 lines × 27 columns.
	BannerArt = `╔╦╗╦ ╦╔╦╗╔═╗  ╔═╗╦  ╦╔═╗╔═╗
 ║ ║ ║ ║ ╠═╣  ╠═╣║  ║╠═╣╚═╗
 ╩ ╚═╝ ╩ ╩ ╩  ╩ ╩╩═╝╩╩ ╩╚═╝`
)

// legalIndent is how far the fine print sits under the bottom menu: two tab
// stops in, so it reads as a footnote to the navigation above it and never as
// another menu line.
const legalIndent = "        "

// renderLegal is the grey fine print, wrapped to the terminal and indented
// under the bottom menu.
func renderLegal(width int) string {
	if width <= 0 {
		width = 100
	}
	w := width - len(legalIndent) - 2
	if w < 32 {
		w = 32
	}
	lines := strings.Split(legalStyle.Width(w).Render(LegalNotice), "\n")
	for i := range lines {
		lines[i] = legalIndent + strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n")
}

// frakturify maps ASCII letters to Unicode Mathematical Bold Fraktur glyphs
// (𝖆–𝖟 / 𝕬–𝖅) — a gothic look without needing a special font. The Bold-Fraktur
// block is contiguous (no Letterlike-Symbols holes), so a plain offset works.
// Non-letters pass through unchanged.
func frakturify(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(0x1D586 + (r - 'a'))
		case r >= 'A' && r <= 'Z':
			b.WriteRune(0x1D56C + (r - 'A'))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// renderBanner is the banner without a live block — what the CLI commands
// (`help`, `install`, `version`) print.
func renderBanner() string { return renderBannerLive("") }

// renderBannerLive returns the banner and, when live is non-empty, the live
// info block directly UNDER the rule with a blank line on each side.
func renderBannerLive(live string) string {
	art := BannerArt
	if art == "" {
		art = AppName
	}
	lines := strings.Split(strings.Trim(art, "\n"), "\n")
	// BANNER_TEXT / BANNER_FONT: the built-in logo only spells the app name,
	// so any other text has to be drawn with the font — block when the font is
	// the logo, otherwise the chosen one. ("logo" was called "default" until
	// 0.12; config normalises the old name on read.)
	text, font := firstNonEmpty(uiBannerText, AppName), uiBannerFont
	if font == "logo" && text != AppName {
		font = "block"
	}
	if f := renderFont(text, font); f != nil {
		lines = f
	}
	for i, l := range lines {
		lines[i] = indent + bannerStyle.Render(l)
	}
	lines[len(lines)-1] += "  " + subtitleStyle.Render("v"+Version)

	var b strings.Builder
	b.WriteString(strings.Join(lines, "\n") + "\n")
	b.WriteString(indent + subtitleStyle.Render(AppDescription) + "\n\n")
	b.WriteString(indent + bylineStyle.Render(frakturify("by Tdwg Entertainment Solutions")+" ®") + "\n\n")
	b.WriteString(rule())
	if live != "" {
		b.WriteString("\n\n" + live)
	}
	return b.String()
}
