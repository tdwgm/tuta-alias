package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/tdwgm/tuta-alias/internal/plusalias"
)

// plusState backs the Plus screen: generate/edit a "+" (sub-)address for a
// base alias, optionally annotate it, then copy + save it LOCALLY. No API
// call — sub-addressing routes server-side.
type plusState struct {
	local  string // base local-part, e.g. "spam"
	domain string // e.g. "example.com"
	suffix textinput.Model
	note   textinput.Model
	focus  int // 0 = suffix, 1 = note
}

// newPlusState builds the screen state for a selected base address. The base
// is split so any existing "+suffix" is dropped before generating a fresh one.
func newPlusState(baseAddr string, suffixLen int) *plusState {
	local, domain := plusalias.SplitBase(baseAddr)
	gen, _ := plusalias.Generate(suffixLen)

	suf := textinput.New()
	suf.SetValue(gen)
	suf.CharLimit = 64
	suf.Prompt = ""
	suf.Focus()

	note := textinput.New()
	note.CharLimit = 128
	note.Prompt = ""
	note.Placeholder = "where it's used (optional)"

	return &plusState{local: local, domain: domain, suffix: suf, note: note, focus: 0}
}

func (p *plusState) result() string {
	return plusalias.Compose(p.local, strings.TrimSpace(p.suffix.Value()), p.domain)
}

func (m model) updatePlus(msg tea.Msg) (model, tea.Cmd) {
	p := m.plusState
	if p == nil {
		m.cur = screenHome
		return m, nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch k.String() {
	case "esc":
		m.cur = screenHome
		m.plusState = nil
		m.errMsg = ""
		return m, nil

	case "tab", "shift+tab", "up", "down":
		// Arrows as well as Tab: the standard moves between rows with ↑/↓,
		// and this screen's two fields are rows.
		p.focus = 1 - p.focus
		if p.focus == 0 {
			p.note.Blur()
			return m, p.suffix.Focus()
		}
		p.suffix.Blur()
		return m, p.note.Focus()

	case "ctrl+r":
		return m.plusRegenerate()

	case "enter":
		suf := strings.TrimSpace(p.suffix.Value())
		if !plusalias.ValidSuffix(suf) {
			m.errMsg = "suffix must be non-empty and use only a-z 0-9 . _ -"
			return m, nil
		}
		if p.domain == "" {
			m.errMsg = "base address has no domain"
			return m, nil
		}
		addr := plusalias.Compose(p.local, suf, p.domain)
		entry := plusalias.Entry{
			Address: addr,
			Note:    strings.TrimSpace(p.note.Value()),
			Created: time.Now().Format("2006-01-02"),
		}
		if err := plusalias.Append(entry); err != nil {
			m.errMsg = "save: " + err.Error()
			return m, nil
		}
		if tool, err := copyToClipboard(addr); err == nil {
			m.flash = "saved + copied " + addr + " (" + tool + ")"
		} else {
			m.flash = "saved " + addr + " (clipboard: " + err.Error() + ")"
		}
		m.cur = screenHome
		m.plusState = nil
		m.errMsg = ""
		return m, nil
	}

	// Route remaining keys to the focused field.
	var cmd tea.Cmd
	if p.focus == 0 {
		p.suffix, cmd = p.suffix.Update(msg)
	} else {
		p.note, cmd = p.note.Update(msg)
	}
	return m, cmd
}

// plusRegenerate draws a new random suffix (^r and the Regenerate menu entry).
func (m model) plusRegenerate() (model, tea.Cmd) {
	p := m.plusState
	if p == nil {
		return m, nil
	}
	gen, err := plusalias.Generate(m.cfg.PlusSuffixLenInt())
	if err != nil {
		m.errMsg = "generate: " + err.Error()
		return m, nil
	}
	p.suffix.SetValue(gen)
	m.errMsg = ""
	return m, nil
}

func (m model) viewPlus() string {
	p := m.plusState
	if p == nil {
		return titleStyle.Render("tuta-alias › Plus") + "\n"
	}
	out := titleStyle.Render("tuta-alias › Plus address") + "\n\n"
	out += "  " + mutedStyle.Render("Base:   ") + txt(p.local+"@"+p.domain) + "\n"
	out += "  " + mutedStyle.Render("Result: ") + okStyle.Render(p.result()) + "\n\n"

	sufMark, noteMark := "  ", "  "
	if p.focus == 0 {
		sufMark = iconStyle.Render("▶ ")
	} else {
		noteMark = iconStyle.Render("▶ ")
	}
	out += sufMark + txt("Suffix: ") + p.suffix.View() + "\n"
	out += noteMark + txt("Note:   ") + p.note.View() + "\n\n"

	out += m.renderBottom() + "\n"
	return out
}
