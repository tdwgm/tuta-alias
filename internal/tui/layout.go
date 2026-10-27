// layout.go — the shared screen grammar of every TUI screen (TUI Scaffold
// standard v2; identical to gu-vpn-pid/layout.go — keep them in sync).
//
//   - Every text line starts at `indent`; rules and dividers start at column 0.
//   - Rows look like tuta-cli: a "▶ " accent marker on the focused row, the
//     label padded to labelWidth in the normal text colour (accent + bold when
//     focused), then the value. Info rows are muted and never focusable.
//   - Section headers in COLOR_HEADER (default ocean blue), titles read "<bin> › <Screen>".
//   - The bottom menu is ONE muted line "Label key ·  Label key …" with Help
//     last; Tab or ↓ moves into it (first entry selected: ▶ accent, label in
//     body text), ←/→ move between entries, Tab/↑ leave it. No navigation line —
//     the keys are listed in Help.
//   - One rule only (under the banner), no footer. Messages are ✓ COLOR_HEADER / ✗ red.
//   - q and Esc mean the same thing on every screen: back one level, and on the
//     top-level status screen, quit.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/charmbracelet/lipgloss"
)

// indent is the left margin of all text (not of rules/dividers).
const indent = "  "

// labelWidth is the padded width of a row label, including its colon.
const labelWidth = 18

// ruleWidth is the width of the dotted banner/footer rules.
const ruleWidth = 44

// ind prefixes every non-empty line of s with indent.
func ind(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = indent + l
		}
	}
	return strings.Join(lines, "\n")
}

// rule is a faint dotted divider at column 0.
func rule() string {
	return bannerRuleStyle.Render(strings.Repeat("·", ruleWidth))
}

// footerRule is the divider above the bottom menu.
func footerRule() string {
	return footerRuleStyle.Render(strings.Repeat("·", ruleWidth))
}

// screenTitle renders "<bin> › <name>".
func screenTitle(name string) string {
	return indent + titleStyle.Render(AppName+" › "+name)
}

// sectionHeader renders a section header in COLOR_HEADER.
func sectionHeader(label string) string {
	return indent + okStyle.Render(label)
}

// Menu effect for the selected entry (MENU_EFFECT): "glitter" sweeps a glint —
// one character in the accent, bold, with a soft accent edge on each side —
// across the label while the menu has focus (menuFrame is advanced by the menu
// tick every ~90 ms); "none" leaves the label plain, so the accent "▶ " alone
// marks the selection. The label is never bold as a whole and never changes
// colour.
var (
	uiMenuEffect = "glitter"
	menuFrame    int
)

var menuEffects = []string{"glitter", "none"}

func glitterLabel(label string, frame int) string {
	r := []rune(label)
	if uiMenuEffect != "glitter" || len(r) == 0 {
		return selectedStyle.Render(label)
	}
	pos := frame%(len(r)+4) - 2 // enters from the left, leaves on the right, then a short pause
	edge := lipgloss.NewStyle().Foreground(AccentColor)
	var b strings.Builder
	for i, c := range r {
		switch {
		case i == pos:
			b.WriteString(keyStyle.Render(string(c)))
		case i == pos-1 || i == pos+1:
			b.WriteString(edge.Render(string(c)))
		default:
			b.WriteString(selectedStyle.Render(string(c)))
		}
	}
	return b.String()
}

// screenMenu names one screen's bottom menu for the Help shortcut list.
type screenMenu struct {
	name string
	btns []button
}

// shortcutRows merges the screens' menus into ONE truthful list keyed by
// shortcut: a key that means the same on every screen is listed once
// ("h: Help"); one that differs lists every meaning with its screens
// ("r: Restart (Status) · Refresh (Log, Doctor)"); one that exists on a single
// screen other than the first names it ("^s: Save (Config)"). q and Esc are
// left to the navigation rows.
func shortcutRows(screens []screenMenu) [][2]string {
	type meaning struct{ label, screen string }
	var order []string
	seen := map[string][]meaning{}
	for _, s := range screens {
		for _, b := range s.btns {
			if b.key == "" || b.key == "q" || b.key == "esc" {
				continue
			}
			if _, ok := seen[b.key]; !ok {
				order = append(order, b.key)
			}
			seen[b.key] = append(seen[b.key], meaning{b.label, s.name})
		}
	}
	var rows [][2]string
	for _, k := range order {
		ms := seen[k]
		key := strings.ReplaceAll(k, "/", " / ")
		same := true
		for _, mm := range ms {
			if mm.label != ms[0].label {
				same = false
			}
		}
		switch {
		case same && len(ms) == 1 && ms[0].screen != screens[0].name:
			rows = append(rows, [2]string{key, ms[0].label + " (" + ms[0].screen + ")"})
		case same:
			rows = append(rows, [2]string{key, ms[0].label})
		default:
			var parts []string
			done := map[string]bool{}
			for _, mm := range ms {
				if done[mm.label] {
					continue
				}
				done[mm.label] = true
				var scr []string
				for _, o := range ms {
					if o.label == mm.label {
						scr = append(scr, o.screen)
					}
				}
				parts = append(parts, mm.label+" ("+strings.Join(scr, ", ")+")")
			}
			rows = append(rows, [2]string{key, strings.Join(parts, " · ")})
		}
	}
	return rows
}

// rowLine renders one focusable row, tuta-cli style: "▶ " in the accent on the
// focused row; the label in the text colour (accent + bold when focused).
func rowLine(focused bool, label, value string) string {
	marker := "  "
	lbl := fgStyle.Render(pad(label+":", labelWidth))
	if focused {
		marker = keyStyle.Render("▶ ")
		lbl = keyStyle.Render(pad(label+":", labelWidth))
	}
	return fitLine(indent + marker + lbl + " " + value)
}

// termWidth is the terminal width (set from tea.WindowSizeMsg; 0 = unknown).
var termWidth int

// fitLine cuts a styled line to the terminal width with "…" so long values
// (paths, DNS lists) never run off the screen.
func fitLine(s string) string {
	if termWidth <= 0 {
		return s
	}
	return ansi.Truncate(s, termWidth-2, "…")
}

// infoLine renders a read-only row aligned with rowLine's labels. Label and
// value are plain text; a value that is unused or unset is a muted "—".
func infoLine(label, value string) string {
	return fitLine(indent + "  " + fgStyle.Render(pad(label+":", labelWidth)) + " " + value)
}

// button is one entry of the bottom menu. key is the shortcut shown under it.
type button struct {
	label  string
	action string
	key    string
}

// renderMenu draws the bottom menu on one line:
//
//	▶ Connect c/d · Restart r · Config o · Log l · Quit q · Help h
//
// Labels are plain text, shortcuts always in COLOR_SHORTCUT (greyish). While
// the menu has focus the selected entry gets the accent "▶ " in front and —
// MENU_EFFECT=glitter — a glint sweeping across its label (glitterLabel); the
// label itself keeps the text colour, never bold or a colour change. The first
// entry owns a fixed 2-column slot (aligned with the row labels above), so the
// line only grows by the marker when a later entry is selected. It wraps onto
// further lines when the terminal is narrow.
func renderMenu(btns []button, sel int, active bool, width int) string {
	if width <= 0 {
		width = 100
	}
	limit := width - len(indent) - 1
	sep := shortcutStyle.Render(" · ")
	var out []string
	cur, w := "", 0
	for i, b := range btns {
		selected := active && i == sel
		label := fgStyle.Render(b.label)
		if selected {
			label = glitterLabel(b.label, menuFrame)
		}
		cell, cw := label, len([]rune(b.label))
		if b.key != "" {
			cell += " " + shortcutStyle.Render(b.key)
			cw += 1 + len([]rune(b.key))
		}
		switch {
		case selected:
			cell, cw = keyStyle.Render("▶ ")+cell, cw+2
		case w == 0:
			cell, cw = "  "+cell, cw+2 // the line's first entry keeps the marker slot
		}
		if w > 0 && w+3+cw > limit {
			out = append(out, indent+cur)
			cur, w = "", 0
			if !selected {
				cell, cw = "  "+cell, cw+2
			}
		}
		if w > 0 {
			cur += sep
			w += 3
		}
		cur += cell
		w += cw
	}
	if cur != "" {
		out = append(out, indent+cur)
	}
	return strings.Join(out, "\n")
}

// keyHint renders a muted "key action  ·  key action" line, wrapped.
func keyHint(width int, pairs ...[2]string) string {
	var parts []string
	for _, p := range pairs {
		parts = append(parts, p[0]+" "+p[1])
	}
	return wrapText(width, mutedStyle, strings.Join(parts, "  ·  "))
}

// flashLine renders a status message: ✓ in COLOR_HEADER for success, ✗ red for errors,
// muted for neutral progress text.
func flashLine(width int, kind msgKind, msg string) string {
	switch kind {
	case msgOK:
		return wrapText(width, okStyle, "✓ "+msg)
	case msgErr:
		return wrapText(width, errStyle, "✗ "+msg)
	}
	return wrapText(width, mutedStyle, msg)
}

// msgKind classifies the transient status message.
type msgKind int

const (
	msgInfo msgKind = iota
	msgOK
	msgErr
)

// wrapText wraps plain text to the screen width with the indent applied.
func wrapText(width int, style lipgloss.Style, text string) string {
	w := width - len(indent) - 2
	if w < 40 {
		w = 40
	}
	lines := strings.Split(style.Width(w).Render(text), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return ind(strings.Join(lines, "\n"))
}

// nextIdx moves i by delta within n items, wrapping.
func nextIdx(i, delta, n int) int {
	if n == 0 {
		return 0
	}
	return ((i+delta)%n + n) % n
}

// scrollWindow returns the part of lines that fits in room rows, keeping the
// focused line (or, with focus -1 = in the menu, the end) visible.
func scrollWindow(lines []string, focus, room int) []string {
	if room <= 0 || len(lines) <= room {
		return lines
	}
	if focus < 0 {
		focus = len(lines) - 1
	}
	inner := max(room, 5) - 2
	return windowAt(lines, focus-inner/2, room)
}

// maxScroll is the largest useful start offset for windowAt.
func maxScroll(n, room int) int {
	if room <= 0 || n <= room {
		return 0
	}
	return n - (max(room, 5) - 2)
}

// windowAt shows lines from start, fitting room rows; clipped ends become muted
// "↑ n more" / "↓ n more" markers, like tuta-cli. room <= 0 = no limit.
func windowAt(lines []string, start, room int) []string {
	if room <= 0 || len(lines) <= room {
		return lines
	}
	room = max(room, 5)
	inner := room - 2
	start = min(max(start, 0), len(lines)-inner)
	end := start + inner
	var out []string
	if start > 0 {
		out = append(out, indent+mutedStyle.Render(fmt.Sprintf("  ↑ %d more", start)))
	} else {
		end++ // no top marker: use its row
	}
	end = min(end, len(lines))
	out = append(out, lines[start:end]...)
	if end < len(lines) {
		out = append(out, indent+mutedStyle.Render(fmt.Sprintf("  ↓ %d more", len(lines)-end)))
	}
	return out
}

// pad right-pads s to n runes.
func pad(s string, n int) string {
	if len([]rune(s)) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len([]rune(s)))
}
