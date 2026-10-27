// diag.go — the two read-only screens: Doctor (the self-diagnostic checklist)
// and Logs (the tail of the log file). Both are opened from Config, both are
// the same thing the CLI prints (`tuta-alias doctor`, `tuta-alias logs`), and
// neither can show anything the CLI would not: the data comes from
// internal/doctor and internal/logx, not from a second implementation.
package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tdwgm/tuta-alias/internal/doctor"
	"github.com/tdwgm/tuta-alias/internal/logx"
)

// logTailLines is how much of the log the screen holds.
const logTailLines = 500

func doctorButtons() []button {
	return []button{
		{label: "Refresh", action: "diag-refresh", key: "^r"},
		{label: "Logs", action: "logs", key: "^l"},
		{label: "Back", action: "back", key: "esc"},
		{label: "Help", action: "help", key: "^h"},
	}
}

func logsButtons() []button {
	return []button{
		{label: "Refresh", action: "diag-refresh", key: "^r"},
		{label: "Clear", action: "logs-clear", key: "^x"},
		// No cross-link back to Doctor: ^d is the text field's
		// delete-forward, and the free keys left have no mnemonic worth the
		// confusion. Esc returns to Config, where Doctor is one row away.
		{label: "Back", action: "back", key: "esc"},
		{label: "Help", action: "help", key: "^h"},
	}
}

// openDoctor runs the checklist and shows it.
func (m *model) openDoctor() {
	if m.cur != screenDoctor && m.cur != screenLogs {
		m.subReturn = m.cur
	}
	m.doctorReport = doctor.Run(Version)
	m.diagScroll = 0
	m.cur = screenDoctor
	m.menuActive, m.menuSel = false, 0
	logx.Debugf("tui: doctor")
}

// openLogs reads the tail of the log and shows it.
func (m *model) openLogs() {
	if m.cur != screenDoctor && m.cur != screenLogs {
		m.subReturn = m.cur
	}
	lines, err := logx.Tail(logTailLines)
	if err != nil {
		m.errMsg = "read log: " + err.Error()
	}
	m.logTail = lines
	m.diagScroll = 0
	m.cur = screenLogs
	m.menuActive, m.menuSel = false, 0
}

// diagLines is the body of whichever diagnostic screen is showing.
func (m model) diagLines() []string {
	if m.cur == screenLogs {
		if len(m.logTail) == 0 {
			return []string{
				indent + mutedStyle.Render(fmt.Sprintf("%s is empty.", shortenHome(logx.PathFor(m.cfg.LogFile)))),
				"",
				indent + mutedStyle.Render(fmt.Sprintf("LOG_LEVEL is %q — set it to error, info or debug in Config.", m.cfg.LogLevel)),
			}
		}
		out := make([]string, 0, len(m.logTail))
		for _, l := range m.logTail {
			out = append(out, fitLine(indent+logLineStyle(l)))
		}
		return out
	}

	var out []string
	out = append(out, sectionHeader("Checklist"))
	for _, c := range m.doctorReport.Checks {
		// The mark follows COLOR_ICON, tinted by the verdict: a failure is
		// red whatever the theme says, because that is not decoration.
		style := iconStyle
		switch c.Level {
		case doctor.LevelFail:
			style = errStyle
		case doctor.LevelWarn:
			style = warnStyle
		}
		out = append(out, fitLine(indent+style.Render(doctor.Mark(c.Level))+" "+
			txt(fmt.Sprintf("%-16s", c.Name))+" "+txt(c.Detail)))
		if c.Fix != "" && c.Level != doctor.LevelOK {
			out = append(out, fitLine(indent+"  "+mutedStyle.Render("→ "+c.Fix)))
		}
	}
	out = append(out, "")
	switch {
	case m.doctorReport.Failed():
		out = append(out, indent+errStyle.Render("Something is wrong — see the ✗ lines."))
	case m.doctorReport.Warned():
		out = append(out, indent+warnStyle.Render("Usable, with warnings (!)."))
	default:
		out = append(out, indent+okStyle.Render("All good."))
	}
	return out
}

// logLineStyle colours a log line by its level word.
func logLineStyle(line string) string {
	switch {
	case strings.Contains(line, " ERROR "):
		return errStyle.Render(line)
	case strings.Contains(line, " DEBUG "):
		return mutedStyle.Render(line)
	}
	return txt(line)
}

// diagRoom is how many body lines fit on a diagnostic screen.
func (m model) diagRoom() int {
	if m.height <= 0 {
		return 0
	}
	room := m.height - 19 // banner + live block + title + rule + menu + fine print
	if room < 5 {
		room = 5
	}
	return room
}

func (m model) diagMaxScroll() int {
	room := m.diagRoom()
	if room == 0 {
		return 0
	}
	return maxScroll(len(m.diagLines()), room)
}

// updateDiag handles the keys of both diagnostic screens. Scrolling and the
// shared keys are in globalKey; this is what is left.
func (m model) updateDiag(msg tea.Msg) (model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "ctrl+r":
		return m.diagRefresh()
	case "ctrl+x":
		if m.cur == screenLogs {
			return m.logsClear()
		}
	case "ctrl+l":
		if m.cur == screenDoctor {
			(&m).openLogs()
		}
	}
	return m, nil
}

// diagRefresh re-runs the checklist or re-reads the log.
func (m model) diagRefresh() (model, tea.Cmd) {
	if m.cur == screenLogs {
		(&m).openLogs()
		m.flash = "log re-read"
		return m, nil
	}
	(&m).openDoctor()
	m.flash = "checks re-run"
	return m, nil
}

// logsClear empties the log file.
func (m model) logsClear() (model, tea.Cmd) {
	if err := logx.Clear(); err != nil {
		m.errMsg = "clear log: " + err.Error()
		return m, nil
	}
	m.logTail = nil
	m.diagScroll = 0
	m.flash = "log cleared"
	return m, nil
}

// viewDiag renders either diagnostic screen.
func (m model) viewDiag() string {
	title, sub := "Doctor", fmt.Sprintf("%s  ·  %s", Version, shortenHome(configPathForHelp()))
	if m.cur == screenLogs {
		title = "Logs"
		sub = fmt.Sprintf("%s  ·  LOG_LEVEL=%s", shortenHome(logx.PathFor(m.cfg.LogFile)), m.cfg.LogLevel)
	}

	var b strings.Builder
	b.WriteString(m.banner() + "\n\n")
	b.WriteString(screenTitle(title) + "\n")
	b.WriteString(indent + mutedStyle.Render(sub) + "\n\n")

	lines := m.diagLines()
	if room := m.diagRoom(); room > 0 && len(lines) > room {
		lines = windowAt(lines, m.diagScroll, room)
	}
	b.WriteString(strings.Join(lines, "\n") + "\n")
	b.WriteString("\n" + m.renderBottom())
	return b.String()
}
