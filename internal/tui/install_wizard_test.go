package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/tdwgm/tuta-alias/internal/config"
)

// The wizard must not drop a key it does not ask about.
//
// It used to build a fresh config.Config from its form fields and save that,
// and config.Save writes every known key: running `tuta-alias install` a
// second time silently cleared TUTA_PROXY — turning a fail-closed proxy into
// direct Tuta traffic, which is the one thing that setting exists to prevent
// — and reset PLUS_SUFFIX_LEN, SHOW_ALIASES_FOR and the whole Theme group.
func TestWizardCommitPreservesKeysItDoesNotOwn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	before := config.Default()
	for k, v := range map[string]string{
		"TUTA_PROXY":       "socks5://127.0.0.1:1055",
		"PLUS_SUFFIX_LEN":  "9",
		"SHOW_ALIASES_FOR": `^spam\.example\.com$`,
		"SHOW_QUOTA":       "true",
		"COLOR_TEXT":       "magenta",
		"THEME":            "green",
		"BANNER_TEXT":      "mail",
		"BANNER_FONT":      "slant",
		"MENU_EFFECT":      "none",
		"LIVE_INFO":        "compact",
	} {
		if err := config.Apply(before, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.Save(before); err != nil {
		t.Fatal(err)
	}

	w := newWizard(nil, os.Stderr, os.Stderr)
	// Install over the running test binary so commit() has nothing to copy.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	w.binPath.SetValue(self)
	w.defaultDomain.SetValue("example.com")
	w.serveHost.SetValue("127.0.0.1")
	w.servePort.SetValue("9090")
	if err := w.commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	after, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	beforeMap, afterMap := config.ToMap(before), config.ToMap(after)
	owned := map[string]bool{
		"DEFAULT_DOMAIN": true, "TUI_ENABLED": true, "CRED_BACKEND": true,
		"BW_ITEM": true, "BW_RUN_ITEM": true, "KEEPASS_DB": true, "KEEPASS_ENTRY": true,
		"CACHE_TTL": true, "SERVE_HOST": true, "SERVE_PORT": true,
		"COMPLETION_BASH": true, "COMPLETION_ZSH": true, "COMPLETION_FISH": true,
		"INSTALL_PATH": true,
	}
	for _, f := range config.Fields {
		if owned[f.Key] {
			continue
		}
		if beforeMap[f.Key] != afterMap[f.Key] {
			t.Errorf("the wizard changed %s, which its form does not ask about: %q -> %q",
				f.Key, beforeMap[f.Key], afterMap[f.Key])
		}
	}
	// And the keys it does own were written.
	if afterMap["DEFAULT_DOMAIN"] != "example.com" || afterMap["SERVE_PORT"] != "9090" {
		t.Errorf("the wizard did not save its own fields: %q %q",
			afterMap["DEFAULT_DOMAIN"], afterMap["SERVE_PORT"])
	}
}

// A value the config file would reject must stop the install, not be written
// and reinterpreted afterwards.
func TestWizardCommitRefusesInvalidValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	w := newWizard(nil, os.Stderr, os.Stderr)
	w.servePort.SetValue("70000")
	if err := w.commit(); err == nil {
		t.Error("commit accepted SERVE_PORT=70000")
	}
}

// Install and uninstall must agree on where the binary is: the wizard lets
// the path be chosen, and uninstall used to look only in ~/.local/bin.
func TestInstallPathIsWhereUninstallLooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	custom := filepath.Join(home, "bin", "tuta-alias")
	cfg := config.Default()
	if err := config.Apply(cfg, "INSTALL_PATH", custom); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(custom), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(custom, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := installBinaryPath(); got != custom {
		t.Fatalf("installBinaryPath() = %q, want %q", got, custom)
	}
	var out strings.Builder
	removeInstall(&out, false)
	if _, err := os.Stat(custom); !os.IsNotExist(err) {
		t.Errorf("uninstall left the binary at %s", custom)
	}
}

// listStop is one position of a choice list: the value the form holds there
// and what the screen draws for it.
type listStop struct {
	value  string
	cells  int   // choices drawn in the list's row
	marked []int // which of them are drawn as the chosen one
}

// walkList presses left until the choice stops moving, then right one entry at
// a time until it stops again, and returns what the form holds and the screen
// draws at each stop. A list stops moving at its ends, so the walk ends there;
// the bound only keeps a broken list from looping forever. A list that can be
// walked to an entry it has no value for makes the wizard panic, which is
// reported here and not allowed to end the whole test run.
func walkList(t *testing.T, w *wizardModel, key, caption string) (stops []listStop) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("the wizard panics while the arrow keys walk the %s list: %v", key, r)
		}
	}()
	held := func() string { return w.ownedValues()[key] }
	step := func(k tea.KeyType) bool {
		before := held()
		w.Update(tea.KeyMsg{Type: k})
		return held() != before
	}
	for range 32 {
		if !step(tea.KeyLeft) {
			break
		}
	}
	stops = []listStop{}
	for range 32 {
		cells, marked := radioMarks(radioRow(w.View(), caption))
		stops = append(stops, listStop{value: held(), cells: cells, marked: marked})
		if !step(tea.KeyRight) {
			break
		}
	}
	return stops
}

// radioRow cuts one choice list out of a rendered screen: the line that starts
// with caption, and the lines the list wrapped onto.
func radioRow(view, caption string) string {
	row := []string{}
	for line := range strings.SplitSeq(ansi.Strip(view), "\n") {
		text := strings.TrimLeft(line, " ▶")
		switch {
		case strings.HasPrefix(text, caption):
			row = append(row, text)
		case len(row) > 0 && strings.HasPrefix(text, "("):
			row = append(row, text)
		case len(row) > 0:
			return strings.Join(row, " ")
		}
	}
	return strings.Join(row, " ")
}

var radioCell = regexp.MustCompile(`\((•| )\)`)

// radioMarks counts the choices drawn in a row and says which of them are
// drawn as the chosen one.
func radioMarks(row string) (cells int, marked []int) {
	marked = []int{}
	for i, m := range radioCell.FindAllStringSubmatch(row, -1) {
		cells++
		if m[1] == "•" {
			marked = append(marked, i)
		}
	}
	return cells, marked
}

// The wizard must offer every value the config file accepts for the keys it
// asks about. It kept its own copies of these lists, and they had already
// drifted: config gained the "vault" backend and the wizard never offered it,
// so the one backend that works without a password manager was invisible on
// a first run, which is the only time the wizard appears.
//
// The wizard's lists are the same slices as config's, so comparing the two
// proves nothing. This drives each list the way a user does, with the arrow
// keys, and reads what the form would write and what the screen draws at every
// stop. A list that cannot be walked to its last entry, draws fewer entries
// than it reaches, or writes something the config file rejects fails here
// whichever slice it was built from.
func TestWizardOffersEveryConfigChoice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	lists := []struct {
		name    string
		key     string
		step    wizardStep
		tabs    int // Tab presses from the first field of the step to the list
		caption string
	}{
		{name: "backend", key: "CRED_BACKEND", step: wizStepCreds, caption: "Backend:"},
		// The cache TTL follows the backend, which has no fields of its own
		// while it is set to None.
		{name: "cache ttl", key: "CACHE_TTL", step: wizStepCreds, tabs: 1, caption: "Cache TTL:"},
		{
			name:    "log level",
			key:     "LOG_LEVEL",
			step:    wizStepDefaults,
			tabs:    slices.Index(wizDefFields, "log"),
			caption: "Log level:",
		},
		{
			name:    "auto update",
			key:     "AUTO_UPDATE",
			step:    wizStepDefaults,
			tabs:    slices.Index(wizDefFields, "update"),
			caption: "Auto update:",
		},
	}
	walked := map[string]bool{}
	for _, c := range lists {
		walked[c.key] = true
		t.Run(c.name, func(t *testing.T) {
			w := newWizard(nil, nil, nil)
			w.step = c.step
			for range c.tabs {
				w.Update(tea.KeyMsg{Type: tea.KeyTab})
			}

			stops := walkList(t, w, c.key, c.caption)
			offered := make([]string, 0, len(stops))
			// What the screen shows, once that differs from what the keys reach.
			drawn := len(stops)
			for i, s := range stops {
				offered = append(offered, s.value)
				if err := config.ValidateKey(c.key, s.value); err != nil {
					t.Errorf("the wizard offers %s=%q, which the config file rejects: %v", c.key, s.value, err)
				}
				if len(s.marked) != 1 || s.marked[0] != i {
					t.Errorf("with %s=%q (index %d of %d) the screen marks %v as chosen, want [%d]",
						c.key, s.value, i, len(stops), s.marked, i)
				}
				if s.cells != len(stops) {
					drawn = s.cells
				}
			}
			if drawn != len(stops) {
				t.Errorf("the arrow keys reach %d choices of %s, but the screen draws %d", len(stops), c.key, drawn)
			}
			missing := []string{}
			for _, want := range config.Choices(c.key) {
				if !slices.Contains(offered, want) {
					missing = append(missing, want)
				}
			}
			if len(missing) > 0 {
				t.Errorf("the config file accepts %q for %s, but the wizard only offers %q", missing, c.key, offered)
			}
		})
	}

	// A list added to the form has to be walked here too. A checkbox needs no
	// walk: it has two states and both are one key press apart.
	boolean := []string{"true", "false"}
	for key := range newWizard(nil, nil, nil).ownedValues() {
		list := config.Choices(key)
		if list == nil || slices.Equal(list, boolean) || walked[key] {
			continue
		}
		t.Errorf("the wizard asks about %s, which has a choice list in config, and this test does not walk it", key)
	}

	// Every backend needs a display name of its own, or the radio shows the
	// bare config value.
	backends := config.Choices("CRED_BACKEND")
	for i, label := range backendLabels(backends) {
		if backends[i] != "" && label == backends[i] {
			t.Errorf("backend %q has no display label", backends[i])
		}
	}
}

// setControl puts value into the control that writes key, the way a user
// leaves the form. A key with no control is reported, so a control cannot be
// added to the wizard without the tests that move every one of them noticing.
func setControl(t *testing.T, w *wizardModel, key, value string) {
	t.Helper()
	offeredAt := func(list []string) int {
		i := slices.Index(list, value)
		if i < 0 {
			t.Fatalf("the wizard cannot offer %s=%q", key, value)
		}
		return i
	}
	switch key {
	case "DEFAULT_DOMAIN":
		w.defaultDomain.SetValue(value)
	case "TUI_ENABLED":
		w.tuiEnabled = value == "true"
	case "CRED_BACKEND":
		w.backendIdx = offeredAt(wizBackendValues)
	case "BW_ITEM":
		w.bwItem.SetValue(value)
	case "BW_RUN_ITEM":
		w.bwRunItem.SetValue(value)
	case "KEEPASS_DB":
		w.kpDB.SetValue(value)
	case "KEEPASS_ENTRY":
		w.kpEntry.SetValue(value)
	case "CACHE_TTL":
		w.ttlIdx = offeredAt(wizTTLChoices)
	case "SERVE_HOST":
		w.serveHost.SetValue(value)
	case "SERVE_PORT":
		w.servePort.SetValue(value)
	case "COMPLETION_BASH":
		w.completionBash = value == "true"
	case "COMPLETION_ZSH":
		w.completionZsh = value == "true"
	case "COMPLETION_FISH":
		w.completionFish = value == "true"
	case "INSTALL_PATH":
		w.binPath.SetValue(value)
	case "TUTA_PROXY":
		w.tutaProxy.SetValue(value)
	case "LOG_LEVEL":
		w.logIdx = offeredAt(config.LogLevels)
	case "AUTO_UPDATE":
		w.updateIdx = offeredAt(wizAutoUpdate)
	default:
		t.Fatalf("the wizard has no control for %s that this test knows how to move", key)
	}
}

// nonDefault returns a valid value for f other than its default, for a key a
// test needs to see survive. It fails for a key it has no value for, which is
// how a new key gets its place in the tests that use it.
func nonDefault(t *testing.T, f config.Field) string {
	t.Helper()
	for _, c := range config.Choices(f.Key) {
		if !strings.EqualFold(c, f.Default) {
			return c
		}
	}
	// The keys with free text have no list to pick from.
	text := map[string]string{
		"PLUS_SUFFIX_LEN":  "9",
		"SHOW_ALIASES_FOR": `^spam\.example\.com$`,
		"LOG_FILE":         "/var/tmp/tuta-alias-wizard-test.log",
		"UPDATE_REF":       "v9.9.9",
		"UPDATE_INTERVAL":  "48h",
		"BANNER_TEXT":      "mail",
	}
	v, ok := text[f.Key]
	if !ok {
		t.Fatalf("no non-default value for %s: add one, so a re-run is shown to keep it", f.Key)
	}
	return v
}

// The wizard writes the keys its form asks about and no others. Run over a
// config the user has tuned, it must leave every key the form does not show
// exactly as it was, and must apply every key it does show.
//
// This used to start from an empty home directory, where there was nothing to
// lose and nothing to change, so it could not tell a wizard that preserves
// keys from one that rebuilds the file. Here every key the form does not ask
// about holds a non-default value, so a key reset to its default shows as a
// change, and every key it does ask about holds a value the test then moves
// its control away from, so a control the wizard ignores shows as unchanged.
func TestWizardOwnedKeysAreRealAndPreserved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// Installing over the running test binary leaves commit() nothing to copy.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// One entry per control on the form: what the file holds for its key
	// before the run, and what the control is moved to.
	controls := map[string]struct{ onDisk, to string }{
		"DEFAULT_DOMAIN":  {onDisk: "old.example.org", to: "example.com"},
		"TUI_ENABLED":     {onDisk: "true", to: "false"},
		"CRED_BACKEND":    {onDisk: "bitwarden", to: "vault"},
		"BW_ITEM":         {onDisk: "old-item", to: "bw-item"},
		"BW_RUN_ITEM":     {onDisk: "old-run-item", to: "run-item"},
		"KEEPASS_DB":      {onDisk: "/old/vault.kdbx", to: "/new/vault.kdbx"},
		"KEEPASS_ENTRY":   {onDisk: "Old/entry", to: "Tuta/main"},
		"CACHE_TTL":       {onDisk: "1h", to: "forever"},
		"SERVE_HOST":      {onDisk: "localhost", to: "127.0.0.2"},
		"SERVE_PORT":      {onDisk: "7777", to: "9090"},
		"COMPLETION_BASH": {onDisk: "false", to: "true"},
		"COMPLETION_ZSH":  {onDisk: "false", to: "true"},
		"COMPLETION_FISH": {onDisk: "true", to: "false"},
		"INSTALL_PATH":    {onDisk: "/old/place/tuta-alias", to: self},
		"TUTA_PROXY":      {onDisk: "socks5://127.0.0.1:1055", to: "socks5://127.0.0.1:2080"},
		"LOG_LEVEL":       {onDisk: "info", to: "debug"},
		"AUTO_UPDATE":     {onDisk: "check", to: "install"},
	}

	// The form and the table list the same keys. A key the form writes with no
	// control here would never be shown to change, and a control here for a
	// key the form does not write would hide that nothing changed.
	owned := newWizard(nil, nil, nil).ownedValues()
	for key := range owned {
		if !config.Known(key) {
			t.Errorf("the wizard writes %s, which is not a config key", key)
		}
		if _, ok := controls[key]; !ok {
			t.Errorf("the wizard writes %s and this test does not move a control for it", key)
		}
	}
	for key := range controls {
		if _, ok := owned[key]; !ok {
			t.Errorf("this test moves a control for %s, which the wizard does not write", key)
		}
	}
	if t.Failed() {
		return
	}

	// The file the wizard is run over. Each value is checked for validity and
	// to survive a save and a load, or it would show nothing about the run.
	onDisk := map[string]string{}
	for _, f := range config.Fields {
		c, asked := controls[f.Key]
		switch {
		case asked && c.onDisk == c.to:
			t.Fatalf("test setup: the control for %s starts where it is moved to", f.Key)
		case asked:
			onDisk[f.Key] = c.onDisk
		default:
			onDisk[f.Key] = nonDefault(t, f)
		}
		if err := config.ValidateKey(f.Key, onDisk[f.Key]); err != nil {
			t.Fatalf("test setup: %v", err)
		}
	}
	seed := config.Default()
	if err := config.ApplyAll(seed, onDisk); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(seed); err != nil {
		t.Fatal(err)
	}
	before, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	beforeMap := config.ToMap(before)
	for _, f := range config.Fields {
		if beforeMap[f.Key] != onDisk[f.Key] {
			t.Fatalf("test setup: %s was saved as %q and loads as %q", f.Key, onDisk[f.Key], beforeMap[f.Key])
		}
	}

	w := newWizard(nil, os.Stderr, os.Stderr)
	for key, c := range controls {
		setControl(t, w, key, c.to)
	}
	// Confirm on the summary screen, which is how a user starts the install.
	w.step = wizStepSummary
	w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if w.step != wizStepDone {
		t.Fatalf("confirming the summary did not install: %s", w.err)
	}

	after, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	afterMap := config.ToMap(after)
	for _, f := range config.Fields {
		c, asked := controls[f.Key]
		switch {
		case asked && afterMap[f.Key] != c.to:
			t.Errorf("the form was set to %s=%q and the saved config holds %q", f.Key, c.to, afterMap[f.Key])
		case !asked && afterMap[f.Key] != beforeMap[f.Key]:
			t.Errorf("the wizard changed %s, which its form does not ask about: %q -> %q",
				f.Key, beforeMap[f.Key], afterMap[f.Key])
		}
	}

	// And the file is one `config check` accepts. This reads the file itself:
	// Load repairs a bad value to its default, so the loaded config cannot
	// show one.
	raw, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	problems, unknown, legacy := config.Validate(doc)
	for _, p := range problems {
		t.Errorf("the wizard wrote a config the validator rejects: %v", p)
	}
	if len(unknown)+len(legacy) > 0 {
		t.Errorf("the wizard wrote keys config does not know (%v) or knows only under a former name (%v)", unknown, legacy)
	}
}

// Every wizard screen has to fit the terminal it is on, and the last one has
// to do what it says.
//
// Both halves come from a real first run on a 61-column terminal: the install
// report printed absolute paths with no wrapping, so the last line was cut
// mid-path, and the screen said "press any key" when only Ctrl-C did
// anything, so the wizard sat there until the user guessed.
func TestWizardFitsANarrowTerminalAndAnyKeyLeaves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	const width = 61
	w := newWizard(nil, nil, nil)
	w.Update(tea.WindowSizeMsg{Width: width, Height: 24})
	w.flash = "wrote " + home + "/.config/tuta-alias/tuta-alias.conf\n" +
		"installed " + home + "/.local/bin/tuta-alias\n" +
		"wrote " + home + "/.local/share/bash-completion/completions/tuta-alias\n"

	for _, step := range []wizardStep{
		wizStepWelcome, wizStepCreds, wizStepDefaults,
		wizStepCompletion, wizStepSummary, wizStepDone,
	} {
		w.step = step
		for _, line := range strings.Split(w.View(), "\n") {
			if n := len([]rune(ansi.Strip(line))); n > width {
				t.Errorf("step %d renders a %d-column line on a %d-column terminal:\n  %s",
					step, n, width, line)
			}
		}
	}

	w.step = wizStepDone
	if _, cmd := w.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}); cmd == nil {
		t.Error("the final screen says any key continues, but a key did nothing")
	}
}

// A path too long for the terminal loses directories from the middle, not its
// name: the file that was written is the part worth reading. It keeps as much
// of the tail as fits, so the result is the longest readable suffix rather
// than the shortest.
func TestElidePathKeepsTheName(t *testing.T) {
	for _, c := range []struct {
		in    string
		width int
	}{
		{"~/.config/tuta-alias/tuta-alias.conf", 60},
		{"~/.local/share/bash-completion/completions/tuta-alias", 40},
		{"/a/b/c/d/e/f/g/tuta-alias", 20},
		{"/home/user/very/deeply/nested/place/for/a/file.conf", 24},
		{"/nodirs", 20},
	} {
		got := elidePath(c.in, c.width)
		if len([]rune(got)) > c.width {
			t.Errorf("elidePath(%q, %d) = %q, which is %d columns wide",
				c.in, c.width, got, len([]rune(got)))
		}
		name := c.in[strings.LastIndex(c.in, "/")+1:]
		if !strings.HasSuffix(got, name) {
			t.Errorf("elidePath(%q, %d) = %q, which lost the file name %q", c.in, c.width, got, name)
		}
		if len([]rune(c.in)) <= c.width && got != c.in {
			t.Errorf("elidePath(%q, %d) = %q, but it already fitted", c.in, c.width, got)
		}
	}
	// Below the point where eliding can say anything useful it gives up
	// rather than returning a row of dots.
	if got := elidePath("short", 3); got != "short" {
		t.Errorf("elidePath on an impossible width = %q, want the input unchanged", got)
	}
}
