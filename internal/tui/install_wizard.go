package tui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/selfinstall"
	"github.com/tdwgm/tuta-alias/internal/vault"
)

// Install wizard — multi-step bubbletea form that walks the user through
// initial configuration and copies the binary to ~/.local/bin/tuta-alias.

type wizardStep int

const (
	wizStepWelcome wizardStep = iota
	wizStepCreds
	wizStepDefaults
	wizStepCompletion
	wizStepSummary
	wizStepDone
)

type wizardModel struct {
	step   wizardStep
	cfg    config.Config
	root   *cobra.Command // for completion-file generation
	stdout io.Writer
	stderr io.Writer

	// Step 1: binary path
	binPath textinput.Model

	// Step 2: credentials
	backendIdx int
	bwItem     textinput.Model
	bwRunItem  textinput.Model
	kpDB       textinput.Model
	kpEntry    textinput.Model
	ttlIdx     int
	credFocus  int // 0=backend, 1..n=conditional fields, n+1=ttl

	// Step 3: defaults, network and updates
	defaultDomain textinput.Model
	tuiEnabled    bool
	serveHost     textinput.Model
	servePort     textinput.Model
	tutaProxy     textinput.Model
	logIdx        int
	updateIdx     int
	defFocus      int

	// Step 4: completion
	completionBash bool
	completionZsh  bool
	completionFish bool
	compFocus      int

	flash string
	err   string
	// width is the terminal width. The wizard never listened for it, so its
	// longest lines (the install paths it reports) ran off a narrow terminal
	// and were cut mid-path.
	width int
}

// The wizard's choices come from the config package, not from a copy. The
// copies had already drifted: config gained the "vault" backend and the
// wizard never offered it, so the one backend you can set up without a
// password manager was invisible on a first run.
var (
	wizBackendValues = config.BackendNames
	wizBackends      = backendLabels(config.BackendNames)
	wizTTLChoices    = config.CacheTTLChoices
	wizAutoUpdate    = config.AutoUpdateModes
)

// backendLabels gives each CRED_BACKEND value its display name.
func backendLabels(values []string) []string {
	name := map[string]string{
		"":          "None",
		"bitwarden": "Bitwarden",
		"bwrun":     "bw-run",
		"keepassxc": "KeePassXC",
		"keyring":   "System keyring",
		"vault":     "Encrypted vault",
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if n, ok := name[v]; ok {
			out = append(out, n)
			continue
		}
		out = append(out, v)
	}
	return out
}

func newWizard(root *cobra.Command, stdout, stderr io.Writer) *wizardModel {
	w := &wizardModel{
		step:           wizStepWelcome,
		cfg:            *config.Default(),
		root:           root,
		stdout:         stdout,
		stderr:         stderr,
		tuiEnabled:     true,
		completionBash: true,
		completionZsh:  true,
	}
	// Load existing config if it's there, so re-running the wizard
	// pre-fills the user's previous values.
	if c, err := config.Load(); err == nil {
		w.cfg = *c
		w.tuiEnabled = c.TUIEnabled
		w.completionBash = c.CompletionBash
		w.completionZsh = c.CompletionZsh
		w.completionFish = c.CompletionFish
	}

	mkInput := func(placeholder, value string, width int) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = placeholder
		ti.CharLimit = 256
		// Width BEFORE SetValue: bubbles works out the visible window when
		// the value is set, so a width applied afterwards is ignored and the
		// field renders its whole value. Measured: a 54-character path in a
		// Width=20 field rendered 58 cells one way round and 23 the other.
		ti.Width = width
		ti.SetValue(value)
		return ti
	}
	w.binPath = mkInput(config.DefaultInstallPath(), w.cfg.BinaryPath(), 40)
	w.binPath.Focus()

	w.bwItem = mkInput("tuta", w.cfg.BWItem, 24)
	w.bwRunItem = mkInput("tuta", w.cfg.BWRunItem, 24)
	w.kpDB = mkInput("~/vault.kdbx", w.cfg.KeePassDB, 40)
	w.kpEntry = mkInput("Tuta/main", w.cfg.KeePassEntry, 24)
	w.defaultDomain = mkInput("example.com", w.cfg.DefaultDomain, 24)
	w.serveHost = mkInput("127.0.0.1", w.cfg.ServeHost, 16)
	w.servePort = mkInput("8088", w.cfg.ServePort, 6)
	w.tutaProxy = mkInput("socks5://127.0.0.1:1055", w.cfg.TutaProxy, 34)
	w.logIdx = indexOf(config.LogLevels, w.cfg.LogLevel)
	w.updateIdx = indexOf(wizAutoUpdate, w.cfg.AutoUpdate)

	w.backendIdx = indexOf(wizBackendValues, w.cfg.CredBackend)
	w.ttlIdx = indexOf(wizTTLChoices, w.cfg.CacheTTL)
	if w.ttlIdx < 0 {
		w.ttlIdx = 0
	}
	return w
}

// RunInstallWizard launches a bubbletea program in install-wizard mode.
// Returns the exit code expected by the caller.
func RunInstallWizard(stdout, stderr io.Writer, root *cobra.Command) int {
	w := newWizard(root, stdout, stderr)
	p := tea.NewProgram(w, tea.WithOutput(stdout))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if w.step == wizStepDone {
		return 0
	}
	return 1
}

func (w *wizardModel) Init() tea.Cmd { return textinput.Blink }

func (w *wizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		w.width = size.Width
		termWidth = size.Width
		w.resizeInputs()
		return w, nil
	}
	k, isKey := msg.(tea.KeyMsg)
	if isKey {
		if k.Type == tea.KeyCtrlC {
			return w, tea.Quit
		}
		// The last screen says "press any key"; it used to mean Ctrl-C only,
		// so the wizard sat there until the user guessed that.
		if w.step == wizStepDone {
			return w, tea.Quit
		}
	}
	switch w.step {
	case wizStepWelcome:
		return w.updateWelcome(msg)
	case wizStepCreds:
		return w.updateCreds(msg)
	case wizStepDefaults:
		return w.updateDefaults(msg)
	case wizStepCompletion:
		return w.updateCompletion(msg)
	case wizStepSummary:
		return w.updateSummary(msg)
	}
	return w, nil
}

func (w *wizardModel) View() string {
	switch w.step {
	case wizStepWelcome:
		return w.viewWelcome()
	case wizStepCreds:
		return w.viewCreds()
	case wizStepDefaults:
		return w.viewDefaults()
	case wizStepCompletion:
		return w.viewCompletion()
	case wizStepSummary:
		return w.viewSummary()
	case wizStepDone:
		return titleStyle.Render("Installation complete.") + "\n\n" +
			w.report() + "\n" +
			mutedStyle.Render("Press any key to continue.") + "\n"
	}
	return "(?)"
}

// report renders what the install did, one line per action, shortened to ~
// and wrapped to the terminal. Paths were printed absolute and unwrapped,
// which on a narrow terminal cut the last one off mid-word.
func (w *wizardModel) report() string {
	width := w.width
	if width <= 0 {
		width = 80
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(w.flash, "\n"), "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(indent + okStyle.Render(elideLine(shortenHome(line), width-len(indent))) + "\n")
	}
	return b.String()
}

// elideLine fits "verb /some/path" into width by dropping directories from
// the MIDDLE of the path. Wrapping a path at the right margin breaks it
// mid-word across three lines, which is how the install report read on a
// narrow terminal; the file name is the part worth keeping.
func elideLine(line string, width int) string {
	if width < 12 || len([]rune(line)) <= width {
		return line
	}
	verb, path, found := strings.Cut(line, " ")
	if !found {
		return elidePath(line, width)
	}
	return verb + " " + elidePath(path, width-len([]rune(verb))-1)
}

// elidePath keeps the first segment and as many trailing segments as fit,
// with … standing for what was dropped.
func elidePath(p string, width int) string {
	if width < 8 || len([]rune(p)) <= width {
		return p
	}
	parts := strings.Split(p, "/")
	if len(parts) < 4 {
		r := []rune(p)
		return "…" + string(r[len(r)-(width-1):])
	}
	head, tail := parts[0], parts[len(parts)-1]
	out := head + "/…/" + tail
	// Add back the directories closest to the file while they fit.
	for i := len(parts) - 2; i > 0; i-- {
		candidate := head + "/…/" + strings.Join(parts[i:], "/")
		if len([]rune(candidate)) > width {
			break
		}
		out = candidate
	}
	if len([]rune(out)) > width {
		r := []rune(tail)
		if len(r) > width-2 {
			r = r[len(r)-(width-2):]
		}
		return "…/" + string(r)
	}
	return out
}

// --- Step 1: Welcome + binary path ---

func (w *wizardModel) updateWelcome(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return w, nil
	}
	switch k.String() {
	case "esc":
		return w, tea.Quit
	case "enter":
		w.step = wizStepCreds
		w.credFocus = 0
		return w, nil
	}
	var cmd tea.Cmd
	w.binPath, cmd = w.binPath.Update(msg)
	return w, cmd
}

func (w *wizardModel) viewWelcome() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("tuta-alias install  ·  Step 1 of 4"))
	b.WriteString("\n\n")
	b.WriteString(w.hint("Welcome. This installs the binary and writes the config.") + "\n\n")
	b.WriteString("Config:  " + elidePath(shortenHome(config.Path()), w.contentWidth()-9) + "\n\n")
	b.WriteString("Binary path:\n  " + w.binPath.View() + "\n\n")
	conflicts := detectInstallConflicts(w.binPath.Value())
	if len(conflicts) > 0 {
		for _, c := range conflicts {
			b.WriteString(errStyle.Render("⚠ "+c) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(w.hint("Enter to continue  ·  Esc to cancel"))
	b.WriteString("\n")
	return b.String()
}

// --- Step 2: Credentials ---

func (w *wizardModel) updateCreds(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return w, nil
	}
	// Focus order: backend(0), cond fields, ttl(last)
	fields := w.credFields()
	switch k.String() {
	case "esc":
		w.step = wizStepWelcome
		w.binPath.Focus()
		return w, nil
	case "enter":
		// On final focus position, advance to next step.
		if w.credFocus >= len(fields)-1 {
			w.step = wizStepDefaults
			w.defFocus = 0
			w.defaultDomain.Focus()
			return w, nil
		}
		w.credFocus++
		w.focusCredField()
		return w, nil
	case "tab", "down":
		w.credFocus = (w.credFocus + 1) % len(fields)
		w.focusCredField()
		return w, nil
	case "shift+tab", "up":
		w.credFocus = (w.credFocus - 1 + len(fields)) % len(fields)
		w.focusCredField()
		return w, nil
	case "left", "h":
		if w.credFocus == 0 && w.backendIdx > 0 {
			w.backendIdx--
		} else if fields[w.credFocus] == "ttl" && w.ttlIdx > 0 {
			w.ttlIdx--
		}
		return w, nil
	case "right", "l":
		if w.credFocus == 0 && w.backendIdx < len(wizBackends)-1 {
			w.backendIdx++
		} else if fields[w.credFocus] == "ttl" && w.ttlIdx < len(wizTTLChoices)-1 {
			w.ttlIdx++
		}
		return w, nil
	}
	// Forward to active textinput.
	switch fields[w.credFocus] {
	case "bw":
		var cmd tea.Cmd
		w.bwItem, cmd = w.bwItem.Update(msg)
		return w, cmd
	case "bwrun":
		var cmd tea.Cmd
		w.bwRunItem, cmd = w.bwRunItem.Update(msg)
		return w, cmd
	case "kpdb":
		var cmd tea.Cmd
		w.kpDB, cmd = w.kpDB.Update(msg)
		return w, cmd
	case "kpentry":
		var cmd tea.Cmd
		w.kpEntry, cmd = w.kpEntry.Update(msg)
		return w, cmd
	}
	return w, nil
}

func (w *wizardModel) credFields() []string {
	out := []string{"backend"}
	switch wizBackendValues[w.backendIdx] {
	case "bitwarden":
		out = append(out, "bw")
	case "bwrun":
		out = append(out, "bwrun")
	case "keepassxc":
		out = append(out, "kpdb", "kpentry")
	}
	out = append(out, "ttl")
	return out
}

func (w *wizardModel) focusCredField() {
	w.bwItem.Blur()
	w.bwRunItem.Blur()
	w.kpDB.Blur()
	w.kpEntry.Blur()
	switch w.credFields()[w.credFocus] {
	case "bw":
		w.bwItem.Focus()
	case "bwrun":
		w.bwRunItem.Focus()
	case "kpdb":
		w.kpDB.Focus()
	case "kpentry":
		w.kpEntry.Focus()
	}
}

func (w *wizardModel) viewCreds() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("tuta-alias install  ·  Step 2 of 4  ·  Credentials"))
	b.WriteString("\n\n")

	fields := w.credFields()

	// Backend radio.
	mark := func(focused bool) string {
		if focused {
			return keyStyle.Render("▶ ")
		}
		return "  "
	}
	b.WriteString(mark(fields[w.credFocus] == "backend") + "Backend:   " +
		radio(wizBackends, w.backendIdx, w.contentWidth()-13, strings.Repeat(" ", 13)) + "\n\n")

	switch wizBackendValues[w.backendIdx] {
	case "bitwarden":
		b.WriteString(mark(fields[w.credFocus] == "bw") + "BW item:  " + w.bwItem.View() + "\n\n")
	case "bwrun":
		b.WriteString(mark(fields[w.credFocus] == "bwrun") + "BW-run item:  " + w.bwRunItem.View() + "\n\n")
	case "keepassxc":
		b.WriteString(mark(fields[w.credFocus] == "kpdb") + "KeePass DB:    " + w.kpDB.View() + "\n")
		b.WriteString(mark(fields[w.credFocus] == "kpentry") + "KeePass entry: " + w.kpEntry.View() + "\n\n")
	}

	b.WriteString(mark(fields[w.credFocus] == "ttl") + "Cache TTL: " +
		radio(wizTTLChoices, w.ttlIdx, w.contentWidth()-13, strings.Repeat(" ", 13)) + "\n\n")
	b.WriteString(w.hint("Tab to move  ·  ←/→ to choose  ·  Enter to continue  ·  Esc back"))
	b.WriteString("\n")
	return b.String()
}

// --- Step 3: Defaults + serve ---

func (w *wizardModel) updateDefaults(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return w, nil
	}
	defs := wizDefFields
	switch k.String() {
	case "esc":
		w.step = wizStepCreds
		return w, nil
	case "enter":
		if w.defFocus >= len(defs)-1 {
			w.step = wizStepCompletion
			w.compFocus = 0
			return w, nil
		}
		w.defFocus++
		w.focusDefField(defs)
		return w, nil
	case "tab", "down":
		w.defFocus = (w.defFocus + 1) % len(defs)
		w.focusDefField(defs)
		return w, nil
	case "shift+tab", "up":
		w.defFocus = (w.defFocus - 1 + len(defs)) % len(defs)
		w.focusDefField(defs)
		return w, nil
	case " ":
		if defs[w.defFocus] == "tui" {
			w.tuiEnabled = !w.tuiEnabled
			return w, nil
		}
	case "left", "h":
		switch defs[w.defFocus] {
		case "log":
			if w.logIdx > 0 {
				w.logIdx--
			}
		case "update":
			if w.updateIdx > 0 {
				w.updateIdx--
			}
		case "tui":
			w.tuiEnabled = !w.tuiEnabled
		}
		return w, nil
	case "right", "l":
		switch defs[w.defFocus] {
		case "log":
			if w.logIdx < len(config.LogLevels)-1 {
				w.logIdx++
			}
		case "update":
			if w.updateIdx < len(wizAutoUpdate)-1 {
				w.updateIdx++
			}
		case "tui":
			w.tuiEnabled = !w.tuiEnabled
		}
		return w, nil
	}
	switch defs[w.defFocus] {
	case "domain":
		var cmd tea.Cmd
		w.defaultDomain, cmd = w.defaultDomain.Update(msg)
		return w, cmd
	case "host":
		var cmd tea.Cmd
		w.serveHost, cmd = w.serveHost.Update(msg)
		return w, cmd
	case "port":
		var cmd tea.Cmd
		w.servePort, cmd = w.servePort.Update(msg)
		return w, cmd
	case "proxy":
		var cmd tea.Cmd
		w.tutaProxy, cmd = w.tutaProxy.Update(msg)
		return w, cmd
	}
	return w, nil
}

// wizDefFields is step 3 in order. TUTA_PROXY, LOG_LEVEL and AUTO_UPDATE are
// here because they are decisions about a fresh install: whether its traffic
// goes through a proxy, whether it keeps a log, and whether it updates
// itself. The rest of the keys are not asked about and the summary says so.
var wizDefFields = []string{"domain", "tui", "host", "port", "proxy", "log", "update"}

func (w *wizardModel) focusDefField(defs []string) {
	w.defaultDomain.Blur()
	w.serveHost.Blur()
	w.servePort.Blur()
	w.tutaProxy.Blur()
	switch defs[w.defFocus] {
	case "domain":
		w.defaultDomain.Focus()
	case "host":
		w.serveHost.Focus()
	case "port":
		w.servePort.Focus()
	case "proxy":
		w.tutaProxy.Focus()
	}
}

// resizeInputs keeps every field inside the terminal. The widths were fixed
// at 40 and 24 columns, which is wider than the whole screen on a narrow
// terminal, so a long path ran past the right edge.
func (w *wizardModel) resizeInputs() {
	room := w.contentWidth() - 18
	if room < 12 {
		room = 12
	}
	fit := func(ti *textinput.Model, want int) {
		if want > room {
			want = room
		}
		ti.Width = want
		// Re-apply, or the window stays the one computed for the old width.
		ti.SetValue(ti.Value())
	}
	fit(&w.binPath, 40)
	fit(&w.kpDB, 40)
	fit(&w.tutaProxy, 34)
	fit(&w.bwItem, 24)
	fit(&w.bwRunItem, 24)
	fit(&w.kpEntry, 24)
	fit(&w.defaultDomain, 24)
	fit(&w.serveHost, 16)
	fit(&w.servePort, 6)
}

// contentWidth is the terminal width, or a sane default before the first
// WindowSizeMsg arrives.
func (w *wizardModel) contentWidth() int {
	if w.width > 0 {
		return w.width
	}
	return 80
}

// hint wraps a step's key reminder, which was written as one long line and
// ran off a narrow terminal.
func (w *wizardModel) hint(text string) string {
	width := w.width
	if width <= 0 {
		width = 80
	}
	return strings.TrimLeft(wrapText(width, mutedStyle, text), " ")
}

// radio renders a choice list, wrapped to width. It used to be one line,
// which was fine for four TTLs and ran off the screen the moment the backend
// list grew to six entries.
func radio(choices []string, idx, width int, cont string) string {
	if width <= 0 {
		width = 80
	}
	var b strings.Builder
	col := 0
	for i, c := range choices {
		label := c
		if label == "" {
			label = "(none)"
		}
		cell := "( ) " + label
		if i == idx {
			cell = iconStyle.Render("(•)") + " " + label
		}
		w := len([]rune(label)) + 4
		if col > 0 && col+w+2 > width {
			b.WriteString("\n" + cont)
			col = len([]rune(cont))
		} else if col > 0 {
			b.WriteString("  ")
			col += 2
		}
		b.WriteString(cell)
		col += w
	}
	return b.String()
}

func (w *wizardModel) viewDefaults() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("tuta-alias install  ·  Step 3 of 4  ·  Defaults"))
	b.WriteString("\n\n")
	mark := func(focused bool) string {
		if focused {
			return keyStyle.Render("▶ ")
		}
		return "  "
	}
	defs := wizDefFields
	b.WriteString(mark(defs[w.defFocus] == "domain") + "Default domain: " + w.defaultDomain.View() + "\n\n")
	tuiVal := "[ ]"
	if w.tuiEnabled {
		tuiVal = keyStyle.Render("[x]")
	}
	b.WriteString(mark(defs[w.defFocus] == "tui") + "TUI as default: " + tuiVal + mutedStyle.Render("  (space to toggle)") + "\n\n")
	b.WriteString(mark(defs[w.defFocus] == "host") + "Serve host:     " + w.serveHost.View() + "\n")
	b.WriteString(mark(defs[w.defFocus] == "port") + "Serve port:     " + w.servePort.View() + "\n\n")
	b.WriteString(mark(defs[w.defFocus] == "proxy") + "Tuta proxy:     " + w.tutaProxy.View() + "\n")
	b.WriteString(w.hint("Empty = direct. All Tuta traffic goes through it, and it is fail-closed.") + "\n\n")
	b.WriteString(mark(defs[w.defFocus] == "log") + "Log level:   " +
		radio(config.LogLevels, w.logIdx, w.contentWidth()-15, strings.Repeat(" ", 15)) + "\n")
	b.WriteString(mark(defs[w.defFocus] == "update") + "Auto update: " +
		radio(wizAutoUpdate, w.updateIdx, w.contentWidth()-15, strings.Repeat(" ", 15)) + "\n\n")
	b.WriteString(w.hint("Tab to move  ·  ←/→ to choose  ·  space to toggle  ·  Enter to continue  ·  Esc back"))
	b.WriteString("\n")
	return b.String()
}

// --- Step 4: Completion ---

func (w *wizardModel) updateCompletion(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return w, nil
	}
	switch k.String() {
	case "esc":
		w.step = wizStepDefaults
		return w, nil
	case "enter":
		w.step = wizStepSummary
		return w, nil
	case "tab", "down":
		w.compFocus = (w.compFocus + 1) % 3
	case "shift+tab", "up":
		w.compFocus = (w.compFocus - 1 + 3) % 3
	case " ":
		switch w.compFocus {
		case 0:
			w.completionBash = !w.completionBash
		case 1:
			w.completionZsh = !w.completionZsh
		default:
			w.completionFish = !w.completionFish
		}
	}
	return w, nil
}

func (w *wizardModel) viewCompletion() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("tuta-alias install  ·  Step 4 of 4  ·  Shell completion"))
	b.WriteString("\n\n")
	mark := func(focused bool) string {
		if focused {
			return keyStyle.Render("▶ ")
		}
		return "  "
	}
	box := func(on bool) string {
		if on {
			return keyStyle.Render("[x]")
		}
		return "[ ]"
	}
	room := w.contentWidth() - 23
	b.WriteString(mark(w.compFocus == 0) + box(w.completionBash) + "  Bash → " + elidePath(shortenHome(bashCompletionPath()), room) + "\n")
	b.WriteString(mark(w.compFocus == 1) + box(w.completionZsh) + "  Zsh  → " + elidePath(shortenHome(zshCompletionPath()), room) + "\n")
	b.WriteString(mark(w.compFocus == 2) + box(w.completionFish) + "  Fish → " + elidePath(shortenHome(fishCompletionPath()), room) + "\n\n")
	b.WriteString(w.hint("Tab to move  ·  space to toggle  ·  Enter to review  ·  Esc back"))
	b.WriteString("\n")
	return b.String()
}

// --- Step 5: Summary + install ---

func (w *wizardModel) updateSummary(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return w, nil
	}
	switch k.String() {
	case "esc":
		w.step = wizStepCompletion
		return w, nil
	case "enter", "y", "Y":
		if err := w.commit(); err != nil {
			w.err = err.Error()
			return w, nil
		}
		w.step = wizStepDone
		return w, nil
	case "n", "N":
		return w, tea.Quit
	}
	return w, nil
}

func (w *wizardModel) viewSummary() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("tuta-alias install  ·  Summary"))
	b.WriteString("\n\n")
	b.WriteString("  Binary:        " + elidePath(shortenHome(w.binPath.Value()), w.contentWidth()-17) + "\n")
	b.WriteString("  Backend:       " + wizBackends[w.backendIdx] + "\n")
	switch wizBackendValues[w.backendIdx] {
	case "bitwarden":
		b.WriteString("  BW item:       " + w.bwItem.Value() + "\n")
	case "bwrun":
		b.WriteString("  BW-run item:   " + w.bwRunItem.Value() + "\n")
	case "keepassxc":
		b.WriteString("  KeePass DB:    " + w.kpDB.Value() + "\n")
		b.WriteString("  KeePass entry: " + w.kpEntry.Value() + "\n")
	}
	b.WriteString("  Cache TTL:     " + wizTTLChoices[w.ttlIdx] + "\n")
	b.WriteString("  Default domain: " + w.defaultDomain.Value() + "\n")
	b.WriteString(fmt.Sprintf("  TUI default:   %v\n", w.tuiEnabled))
	b.WriteString("  Serve:         " + w.serveHost.Value() + ":" + w.servePort.Value() + "\n")
	proxy := w.tutaProxy.Value()
	if strings.TrimSpace(proxy) == "" {
		proxy = "direct"
	}
	b.WriteString("  Network:       " + proxy + "\n")
	b.WriteString("  Log level:     " + config.LogLevels[w.logIdx] + "\n")
	b.WriteString("  Auto update:   " + wizAutoUpdate[w.updateIdx] + "\n")
	b.WriteString(fmt.Sprintf("  Completion:    bash=%v zsh=%v fish=%v\n", w.completionBash, w.completionZsh, w.completionFish))
	b.WriteString("\n")
	if w.err != "" {
		b.WriteString(errStyle.Render("✗ "+w.err) + "\n")
	}
	if wizBackendValues[w.backendIdx] == "vault" && !vault.Exists() {
		b.WriteString(w.hint("The vault does not exist yet: run `tuta-alias vault set` after this.") + "\n")
	}
	b.WriteString(w.hint("Everything else (theme, quota, alias filter, plus length) is in Config.") + "\n\n")
	b.WriteString(keyStyle.Render("[Enter / y]") + " install   " +
		keyStyle.Render("[Esc]") + " back   " +
		keyStyle.Render("[n]") + " cancel\n")
	return b.String()
}

// --- commit: actually do the install ---

func (w *wizardModel) commit() error {
	// Start from what is already on disk (newWizard loaded it into w.cfg) and
	// change only the keys this form owns.
	//
	// This used to build a fresh config.Config from the form fields, and
	// config.Save writes every known key: re-running the wizard therefore
	// cleared TUTA_PROXY — silently turning a fail-closed proxy into direct
	// Tuta traffic — and reset PLUS_SUFFIX_LEN, SHOW_ALIASES_FOR and the whole
	// Theme group. Going through config.ApplyAll also means a new key is
	// preserved here automatically instead of having to be remembered.
	cfg := w.cfg
	owned := w.ownedValues()
	// Refuse a value the program could not honour instead of writing it and
	// reinterpreting it later.
	for _, k := range []string{"DEFAULT_DOMAIN", "SERVE_HOST", "SERVE_PORT", "CACHE_TTL", "INSTALL_PATH", "TUTA_PROXY", "LOG_LEVEL", "AUTO_UPDATE"} {
		if err := config.ValidateKey(k, owned[k]); err != nil {
			return err
		}
	}
	if err := config.ApplyAll(&cfg, owned); err != nil {
		return err
	}
	if err := config.Save(&cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	w.cfg = cfg
	w.flash = "wrote " + config.Path() + "\n"

	log, err := selfinstall.Apply(&cfg, w.root)
	for _, line := range log {
		w.flash += line + "\n"
	}
	if err != nil {
		return err
	}
	if warn := selfinstall.PathWarning(cfg.BinaryPath()); warn != "" {
		w.flash += "\n" + errStyle.Render("⚠ "+strings.TrimSpace(warn)) + "\n"
	}
	return nil
}

// ownedValues is every config key this form asks about, with the value the
// form holds for it now. commit writes these and no other key, which is what
// lets a re-run leave alone everything the form never showed.
func (w *wizardModel) ownedValues() map[string]string {
	dst := strings.TrimSpace(w.binPath.Value())
	if dst == "" {
		dst = config.DefaultInstallPath()
	}
	installPath := dst
	if dst == config.DefaultInstallPath() {
		installPath = "" // stay at the default rather than pinning the path
	}
	return map[string]string{
		"DEFAULT_DOMAIN":  strings.TrimSpace(w.defaultDomain.Value()),
		"TUI_ENABLED":     boolWord(w.tuiEnabled),
		"CRED_BACKEND":    wizBackendValues[w.backendIdx],
		"BW_ITEM":         strings.TrimSpace(w.bwItem.Value()),
		"BW_RUN_ITEM":     strings.TrimSpace(w.bwRunItem.Value()),
		"KEEPASS_DB":      strings.TrimSpace(w.kpDB.Value()),
		"KEEPASS_ENTRY":   strings.TrimSpace(w.kpEntry.Value()),
		"CACHE_TTL":       wizTTLChoices[w.ttlIdx],
		"SERVE_HOST":      strings.TrimSpace(w.serveHost.Value()),
		"SERVE_PORT":      strings.TrimSpace(w.servePort.Value()),
		"COMPLETION_BASH": boolWord(w.completionBash),
		"COMPLETION_ZSH":  boolWord(w.completionZsh),
		"COMPLETION_FISH": boolWord(w.completionFish),
		"INSTALL_PATH":    installPath,
		"TUTA_PROXY":      strings.TrimSpace(w.tutaProxy.Value()),
		"LOG_LEVEL":       config.LogLevels[w.logIdx],
		"AUTO_UPDATE":     wizAutoUpdate[w.updateIdx],
	}
}

// boolWord renders a checkbox for the config file.
func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// --- helpers used by wizard ---

func detectInstallConflicts(target string) []string {
	var out []string
	if _, err := os.Stat(target); err == nil {
		out = append(out, "file already exists: "+target+" (will be overwritten)")
	}
	if other, err := exec.LookPath("tuta-alias"); err == nil {
		if abs, aerr := filepath.Abs(target); aerr == nil && other != abs && other != target {
			// A different tuta-alias earlier in $PATH keeps winning after the
			// install, which looks exactly like "my change did nothing".
			out = append(out, "another tuta-alias is earlier in $PATH: "+other)
		}
	}
	return out
}
