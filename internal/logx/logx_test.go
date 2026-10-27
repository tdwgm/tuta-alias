package logx

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/tdwgm/tuta-alias/internal/config"
)

// isolate points HOME and XDG_CACHE_HOME at fresh temp dirs and puts the
// package-level logger back to "off" afterwards, so one test's level or path
// cannot leak into the next one (the logger is global state).
func isolate(t *testing.T) (home, cache string) {
	t.Helper()
	home, cache = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Cleanup(func() { Setup(nil) })
	return home, cache
}

// setup configures the logger the way the CLI does: through a Config.
func setup(t *testing.T, level, file string) {
	t.Helper()
	cfg := config.Default()
	if err := config.Apply(cfg, "LOG_LEVEL", level); err != nil {
		t.Fatal(err)
	}
	if err := config.Apply(cfg, "LOG_FILE", file); err != nil {
		t.Fatal(err)
	}
	Setup(cfg)
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", p, err)
	}
	return string(raw)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// The log must not appear on a machine that did not ask for one. With
// LOG_LEVEL=off not even the directory may be created, and that has to hold
// for both the default location and a LOG_FILE override.
func TestOffWritesNothingAnywhere(t *testing.T) {
	for _, tc := range []struct {
		name    string
		logFile func(home string) string
	}{
		{"default path", func(string) string { return "" }},
		{"LOG_FILE override", func(home string) string { return filepath.Join(home, "elsewhere", "my.log") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cache := isolate(t)
			setup(t, "off", tc.logFile(home))

			Errorf("%s", "should-not-appear")
			Infof("%s", "should-not-appear")
			Debugf("%s", "should-not-appear")

			if Enabled() {
				t.Error("Enabled() is true although LOG_LEVEL is off")
			}
			if exists(Path()) {
				t.Errorf("log file %s was created although LOG_LEVEL is off", Path())
			}
			for _, d := range []string{home, cache} {
				entries, err := os.ReadDir(d)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Errorf("logging is off but %s now holds %d entries", d, len(entries))
				}
			}
		})
	}
}

// Without the filter a user who sets LOG_LEVEL=error would get every debug
// line (and whatever addresses they mention) in the file.
func TestLevelFiltering(t *testing.T) {
	for _, tc := range []struct {
		level                     string
		wantErr, wantInf, wantDbg bool
	}{
		{"error", true, false, false},
		{"info", true, true, false},
		{"debug", true, true, true},
	} {
		t.Run(tc.level, func(t *testing.T) {
			isolate(t)
			setup(t, tc.level, "")

			Errorf("%s", "marker-error")
			Infof("%s", "marker-info")
			Debugf("%s", "marker-debug")

			if !Enabled() {
				t.Error("Enabled() is false although logging was switched on")
			}
			if LevelName() != tc.level {
				t.Errorf("LevelName() is %q after setting LOG_LEVEL=%s", LevelName(), tc.level)
			}
			got := readFile(t, Path())
			for _, m := range []struct {
				marker string
				want   bool
			}{
				{"marker-error", tc.wantErr},
				{"marker-info", tc.wantInf},
				{"marker-debug", tc.wantDbg},
			} {
				if has := strings.Contains(got, m.marker); has != m.want {
					t.Errorf("at LOG_LEVEL=%s, %s written=%v, expected %v\nlog:\n%s", tc.level, m.marker, has, m.want, got)
				}
			}
		})
	}
}

// Each line carries its level so `tuta-alias logs` output can be grepped.
func TestLinesCarryTheirLevelTag(t *testing.T) {
	isolate(t)
	setup(t, "debug", "")
	Errorf("%s", "a")
	Infof("%s", "b")
	Debugf("%s", "c")

	lines, err := Tail(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 log lines, got %d: %q", len(lines), lines)
	}
	for i, tag := range []string{"ERROR", "INFO", "DEBUG"} {
		if !strings.Contains(lines[i], " "+tag+" ") {
			t.Errorf("line %d has no %s tag: %q", i, tag, lines[i])
		}
	}
}

// The destination is the one thing a user can move, and the default has to be
// next to the session and api-token (XDG cache), not in the config dir.
func TestLogFileOverrideAndDefault(t *testing.T) {
	t.Run("LOG_FILE wins over the default", func(t *testing.T) {
		home, cache := isolate(t)
		custom := filepath.Join(home, "custom", "x.log")
		setup(t, "error", custom)

		if Path() != custom {
			t.Errorf("Path() = %q, but LOG_FILE asked for %q", Path(), custom)
		}
		Errorf("%s", "hello")
		if !exists(custom) {
			t.Errorf("nothing was written to the LOG_FILE %s", custom)
		}
		if exists(filepath.Join(cache, "tuta-alias", "tuta-alias.log")) {
			t.Error("the default log was written although LOG_FILE was set")
		}
	})

	t.Run("empty LOG_FILE uses XDG_CACHE_HOME", func(t *testing.T) {
		_, cache := isolate(t)
		setup(t, "error", "")

		want := filepath.Join(cache, "tuta-alias", "tuta-alias.log")
		if Path() != want {
			t.Errorf("Path() = %q, expected %q", Path(), want)
		}
		Errorf("%s", "hello")
		if !exists(want) {
			t.Errorf("nothing was written to %s", want)
		}
	})

	t.Run("no XDG_CACHE_HOME falls back to ~/.cache", func(t *testing.T) {
		home, _ := isolate(t)
		t.Setenv("XDG_CACHE_HOME", "")
		want := filepath.Join(home, ".cache", "tuta-alias", "tuta-alias.log")
		if got := DefaultPath(); got != want {
			t.Errorf("DefaultPath() = %q with XDG_CACHE_HOME unset, expected %q", got, want)
		}
	})
}

// The log can hold an address or a config path, so it must be no more
// readable than the session file.
func TestLogIsPrivate(t *testing.T) {
	isolate(t)
	setup(t, "error", "")
	Errorf("%s", "someone@example.com")

	fi, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("log file mode is %o, must be 600 because it can hold an email address", perm)
	}
	di, err := os.Stat(filepath.Dir(Path()))
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("log directory mode is %o, it must not be open to group or others", perm)
	}
}

// Setup(nil) is what a caller with no config gets; it must silence a logger
// that an earlier Setup switched on, not leave the old level running.
func TestSetupNilTurnsLoggingOff(t *testing.T) {
	isolate(t)
	setup(t, "debug", "")
	Errorf("%s", "before")
	p := Path()
	size := Size()

	Setup(nil)
	Errorf("%s", "after")
	Debugf("%s", "after")

	if Enabled() {
		t.Error("Enabled() is true after Setup(nil)")
	}
	if LevelName() != "off" {
		t.Errorf("LevelName() = %q after Setup(nil), expected off", LevelName())
	}
	if Size() != size {
		t.Errorf("the log grew from %d to %d bytes after Setup(nil)", size, Size())
	}
	if strings.Contains(readFile(t, p), "after") {
		t.Error("a line was written after Setup(nil)")
	}
}

// A typo in LOG_LEVEL must land on the quiet side. Reversing this would turn
// "LOG_LEVEL=debg" into debug output containing addresses.
func TestParseLevel(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Level
	}{
		{"off", LevelOff},
		{"error", LevelError},
		{"info", LevelInfo},
		{"debug", LevelDebug},
		{"DEBUG", LevelDebug},
		{"  Info  ", LevelInfo},
		{"", LevelOff},
		{"debg", LevelOff},
		{"verbose", LevelOff},
		{"warn", LevelOff},
		{"errors", LevelOff},
		{"1", LevelOff},
	} {
		if got := ParseLevel(tc.in); got != tc.want {
			t.Errorf("ParseLevel(%q) = %d, expected %d (unknown words must mean off)", tc.in, got, tc.want)
		}
	}
}

// Levels is indexed by Level in LevelName, and config validates LOG_LEVEL
// against its own copy; if the three drift apart the config screen offers a
// level the logger reads as off.
func TestLevelListsAgree(t *testing.T) {
	if !reflect.DeepEqual(Levels, config.LogLevels) {
		t.Errorf("logx.Levels %v and config.LogLevels %v differ", Levels, config.LogLevels)
	}
	for i, name := range Levels {
		if got := ParseLevel(name); got != Level(i) {
			t.Errorf("ParseLevel(%q) = %d, but it sits at index %d in Levels", name, got, i)
		}
	}
}

// Tail feeds `tuta-alias logs` and the Logs screen: the order is what makes
// the output readable top to bottom.
func TestTail(t *testing.T) {
	isolate(t)
	setup(t, "error", "")
	for i := 1; i <= 5; i++ {
		Errorf("line-%d", i)
	}

	for _, tc := range []struct {
		n    int
		want []string
	}{
		{2, []string{"line-4", "line-5"}},
		{5, []string{"line-1", "line-2", "line-3", "line-4", "line-5"}},
		{50, []string{"line-1", "line-2", "line-3", "line-4", "line-5"}},
	} {
		got, err := Tail(tc.n)
		if err != nil {
			t.Fatalf("Tail(%d): %v", tc.n, err)
		}
		if len(got) != len(tc.want) {
			t.Errorf("Tail(%d) returned %d lines, expected %d: %q", tc.n, len(got), len(tc.want), got)
			continue
		}
		for i, suffix := range tc.want {
			if !strings.HasSuffix(got[i], suffix) {
				t.Errorf("Tail(%d)[%d] = %q, expected a line ending in %q (oldest first)", tc.n, i, got[i], suffix)
			}
		}
	}
}

// Opening the Logs screen before anything was ever logged is the normal first
// run; it must show "empty", not an error.
func TestTailWithoutLogIsNilNotError(t *testing.T) {
	isolate(t)
	setup(t, "error", "")

	got, err := Tail(10)
	if err != nil {
		t.Errorf("Tail with no log file returned error %v", err)
	}
	if got != nil {
		t.Errorf("Tail with no log file returned %q, expected nil", got)
	}

	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Tail(10); err != nil || got != nil {
		t.Errorf("Tail of an empty log = %q, %v; expected nil, nil", got, err)
	}
}

func TestSize(t *testing.T) {
	isolate(t)
	setup(t, "error", "")
	if got := Size(); got != -1 {
		t.Errorf("Size() = %d with no log, expected -1 so callers can tell it from an empty file", got)
	}
	Errorf("%s", "x")
	if got := Size(); got <= 0 {
		t.Errorf("Size() = %d after a line was written", got)
	}
}

// "Clear log" in the TUI must leave nothing behind: a stale .1 would keep
// old addresses on disk after the user asked for them to be gone.
func TestClear(t *testing.T) {
	t.Run("removes the log and the rotated generation", func(t *testing.T) {
		isolate(t)
		setup(t, "error", "")
		Errorf("%s", "live")
		rotated := Path() + ".1"
		if err := os.WriteFile(rotated, []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := Clear(); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if exists(Path()) {
			t.Error("the live log survived Clear")
		}
		if exists(rotated) {
			t.Error("the rotated .1 log survived Clear")
		}
	})

	t.Run("nothing to remove is not an error", func(t *testing.T) {
		isolate(t)
		setup(t, "error", "")
		if err := Clear(); err != nil {
			t.Errorf("Clear with no log returned %v", err)
		}
		Errorf("%s", "x")
		_ = Clear()
		if err := Clear(); err != nil {
			t.Errorf("a second Clear returned %v", err)
		}
	})

	t.Run("a log can be started again afterwards", func(t *testing.T) {
		isolate(t)
		setup(t, "error", "")
		Errorf("%s", "first")
		if err := Clear(); err != nil {
			t.Fatal(err)
		}
		Errorf("%s", "second")
		got := readFile(t, Path())
		if strings.Contains(got, "first") || !strings.Contains(got, "second") {
			t.Errorf("after Clear the log should hold only the new line, got %q", got)
		}
	})
}

// Without rotation the log grows for as long as the program is used; with a
// bug in it the user loses the lines that explain the crash. Driven through
// real writes so the check and the append are exercised together.
func TestRotationOverOneMiB(t *testing.T) {
	isolate(t)
	setup(t, "error", "")

	Errorf("%s", "FIRST-LINE-MARKER")
	chunk := strings.Repeat("a", 4096)
	for written := 0; written <= maxBytes+len(chunk); written += len(chunk) {
		Errorf("%s", chunk)
	}
	Errorf("%s", "LAST-LINE-MARKER")

	rotated := Path() + ".1"
	if !exists(rotated) {
		t.Fatalf("more than 1 MiB was logged but %s does not exist", rotated)
	}
	if !strings.Contains(readFile(t, rotated), "FIRST-LINE-MARKER") {
		t.Error("the oldest lines are not in the .1 generation")
	}
	live := readFile(t, Path())
	if strings.Contains(live, "FIRST-LINE-MARKER") {
		t.Error("the live log still holds lines that should have moved to .1")
	}
	if !strings.Contains(live, "LAST-LINE-MARKER") {
		t.Error("the newest line is missing from the live log after rotation")
	}
	if len(live) >= maxBytes {
		t.Errorf("the live log is %d bytes after rotation, it should have restarted small", len(live))
	}
}

// The boundary matters in both directions: rotating early throws away
// history for no reason, rotating late lets the file grow past its cap. The
// second generation is replaced, not kept forever.
func TestRotationBoundary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		size       int
		wantRotate bool
	}{
		{"just under the cap is kept", maxBytes - 1, false},
		{"exactly at the cap rotates", maxBytes, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			setup(t, "error", "")
			p := Path()
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			seed := "OLD-CONTENT\n" + strings.Repeat("x", tc.size-len("OLD-CONTENT\n"))
			if err := os.WriteFile(p, []byte(seed), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p+".1", []byte("ANCIENT\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			Errorf("%s", "fresh")

			if tc.wantRotate {
				if got := readFile(t, p+".1"); !strings.HasPrefix(got, "OLD-CONTENT") {
					t.Errorf("the .1 generation should hold the old log, begins %q", got[:min(len(got), 20)])
				}
				live := readFile(t, p)
				if strings.Contains(live, "OLD-CONTENT") || !strings.Contains(live, "fresh") {
					t.Errorf("the live log should restart with only the new line, got %d bytes", len(live))
				}
			} else {
				if got := readFile(t, p+".1"); got != "ANCIENT\n" {
					t.Errorf("rotated early: .1 now holds %q", got[:min(len(got), 20)])
				}
				if !strings.HasPrefix(readFile(t, p), "OLD-CONTENT") {
					t.Error("the live log was reset below the size cap")
				}
			}
		})
	}
}

// Goroutines (clipboard, keyring, the TUI) all log at once. Interleaved or
// lost lines would make the log useless exactly when something is racing.
func TestConcurrentWritesStayWholeLines(t *testing.T) {
	isolate(t)
	setup(t, "error", "")

	const workers, each = 8, 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				Errorf("worker-%d-line-%d-end", w, i)
			}
		}()
	}
	wg.Wait()

	lines, err := Tail(workers * each * 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != workers*each {
		t.Fatalf("expected %d lines from concurrent writers, found %d", workers*each, len(lines))
	}
	for _, l := range lines {
		if !strings.HasSuffix(l, "-end") || strings.Count(l, "worker-") != 1 {
			t.Errorf("a line was cut or interleaved: %q", l)
		}
	}
}

// controlIn returns the first character in s that a terminal or a line reader
// would act on instead of show, or -1 when there is none (NUL is a control
// character too, so 0 cannot mean "none"). A byte that is not valid UTF-8
// counts: a terminal in 8-bit mode reads a stray 0x9b as CSI.
func controlIn(s string) rune {
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == utf8.RuneError {
			return r
		}
	}
	return -1
}

// What reaches the log is not always this program's own text: an error quoted
// from a server, git's stderr and an address typed into a form all end up in
// it. A raw newline in such a value forges a second line with a timestamp and
// a level of the sender's choosing, a carriage return overwrites the line
// before it, and an escape sequence is printed to the terminal by
// `tuta-alias logs` (OSC 52 writes the clipboard). One call must be one line,
// the file must hold no control character, and the escape must stay visible so
// the line still shows that something was there.
func TestLogLineCannotBeForged(t *testing.T) {
	for _, tc := range []struct{ name, value, visible string }{
		{"a newline forges a line", "HTTP 500\n2026-10-27 12:00:00 INFO logged in as admin@example.com", `HTTP 500\n2026-10-27 12:00:00 INFO`},
		{"a carriage return overwrites the line", "ok\r2026-10-27 12:00:00 INFO fine", `ok\r2026-10-27`},
		{"an OSC 52 clipboard write", "x\x1b]52;c;ZXZpbA==\x07y", `x\x1b]52;c;ZXZpbA==\x07y`},
		{"a CSI sequence", "x\x1b[2J\x1b[31my", `x\x1b[2J\x1b[31my`},
		{"a C1 control", "x\u009by", `x\x9by`},
		{"a stray 0x9b byte", "x\x9by", `x\x9by`},
		{"a Unicode line separator", "x\u2028y\u2029z", `x\u2028y\u2029z`},
		{"a NUL", "x\x00y", `x\x00y`},
		{"a DEL", "x\x7fy", `x\x7fy`},
		{"a tab", "x\ty", `x\ty`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			setup(t, "error", "")
			Errorf("login: %v", tc.value)

			raw := readFile(t, Path())
			if n := strings.Count(raw, "\n"); n != 1 {
				t.Errorf("one call wrote %d lines, the value forged the rest:\n%q", n, raw)
			}
			if c := controlIn(strings.TrimSuffix(raw, "\n")); c != -1 {
				t.Errorf("the log holds the control character %U:\n%q", c, raw)
			}
			if !strings.Contains(raw, tc.visible) {
				t.Errorf("the escaped value should still be readable as %q:\n%q", tc.visible, raw)
			}
		})
	}
}

// Text that is already printable must come out exactly as it went in, or
// every address and path in the log would change.
func TestLogLineKeepsPrintableText(t *testing.T) {
	isolate(t)
	setup(t, "error", "")
	const msg = "created t\u00e4st-\u00e5\u00e4\u00f6@example.com in C:\\Users\\x\\file.txt: \"quoted\" \\n is two characters here \U0001F600"
	Errorf("%s", msg)
	got := readFile(t, Path())
	if !strings.HasSuffix(got, " "+msg+"\n") {
		t.Errorf("printable text was changed:\n got  %q\n want a line ending in %q", got, msg)
	}
}

// Lines written before the writer escaped them (an older version, or another
// program sharing LOG_FILE) are read back through Tail, and `logs` and the
// Logs screen print whatever Tail returns.
func TestTailNeutralisesControlCharactersAlreadyInTheFile(t *testing.T) {
	isolate(t)
	setup(t, "error", "")
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	old := "2026-10-27 12:00:00 ERROR old \x1b]52;c;ZXZpbA==\x07 line\r\n2026-10-27 12:00:01 ERROR next\n"
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Tail(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Tail returned %d lines, want 2: %q", len(got), got)
	}
	for _, l := range got {
		if c := controlIn(l); c != -1 {
			t.Errorf("Tail returned a line with the control character %U: %q", c, l)
		}
	}
	if !strings.Contains(got[0], `old \x1b]52;c;ZXZpbA==\x07 line`) {
		t.Errorf("the escaped line should still be readable: %q", got[0])
	}
}

// The XDG spec says a relative XDG_CACHE_HOME is invalid and must be ignored.
// Honoured, it puts the log, and the token and session that live beside it, in
// whatever directory the command ran in. "~/.cache", set where nothing expands
// the tilde (systemd's environment.d, /etc/environment), is the usual way to
// get one, and it creates a directory literally named "~".
func TestDefaultPathIgnoresARelativeCacheHome(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "~/.cache")
	want := filepath.Join(home, ".cache", "tuta-alias", "tuta-alias.log")
	if got := DefaultPath(); got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
}

// With no usable home directory there is no default log. It is not a log in
// the working directory, which is where a path built from an empty home lands.
func TestDefaultPathWithoutAHomeIsEmpty(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_CACHE_HOME", "")
	for _, home := range []string{"", "relative/home"} {
		t.Setenv("HOME", home)
		if got := DefaultPath(); got != "" {
			t.Errorf("DefaultPath with HOME=%q = %q, want \"\"", home, got)
		}
	}
}

// Clear with no log path at all must not turn "" + ".1" into a file called
// ".1" in the working directory and delete it.
func TestClearWithoutAPathTouchesNothing(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	cwd := t.TempDir()
	t.Chdir(cwd)
	bystander := filepath.Join(cwd, ".1")
	if err := os.WriteFile(bystander, []byte("not the log"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Clear(); err != nil {
		t.Fatal(err)
	}
	if !exists(bystander) {
		t.Error("Clear deleted a file called .1 from the working directory")
	}
}
