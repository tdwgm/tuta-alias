// Package logx is the one log file: a small levelled logger the CLI, the TUI
// and the background goroutines all write to.
//
// It exists because the program had nowhere to put the errors it decides not
// to show. A clipboard helper that is missing, a keyring that is locked, a
// config save that failed while a screen was repainting: each was either
// swallowed or flashed for a second and lost. The log is where they go now,
// and `tuta-alias logs` and Config → Logs are how they are read back.
//
// Writing is off unless LOG_LEVEL says otherwise, so nothing is created on a
// machine that did not ask for it.
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tdwgm/tuta-alias/internal/config"
)

// Level is how much is written.
type Level int

const (
	LevelOff Level = iota
	LevelError
	LevelInfo
	LevelDebug
)

// Levels are the accepted LOG_LEVEL values, least to most verbose.
var Levels = []string{"off", "error", "info", "debug"}

// ParseLevel turns a LOG_LEVEL value into a Level. Anything unknown is off,
// which is the safe direction: a typo must not start writing debug output.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "error":
		return LevelError
	case "info":
		return LevelInfo
	case "debug":
		return LevelDebug
	}
	return LevelOff
}

// maxBytes is when the log is rotated to .1 (one generation, no cron).
const maxBytes = 1 << 20

var (
	mu    sync.Mutex
	level = LevelOff
	path  string
)

// DefaultPath is the log file when LOG_FILE is empty: next to the session and
// the api-token, because it is per-machine state, not configuration.
//
// Only an absolute directory counts. The XDG spec says a relative
// XDG_CACHE_HOME is invalid and must be ignored, and a relative path here puts
// the log in whatever directory the command ran in, away from the session and
// the token it is supposed to sit next to. The home directory has to be
// absolute for the same reason. "" means there is no default.
func DefaultPath() string {
	if x := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(x) {
		return filepath.Join(x, "tuta-alias", "tuta-alias.log")
	}
	h, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(h) {
		return ""
	}
	return filepath.Join(h, ".cache", "tuta-alias", "tuta-alias.log")
}

// Setup points the logger at what cfg asks for. Safe to call again (the TUI
// calls it after a config reload) and safe to never call (level stays off).
func Setup(cfg *config.Config) {
	mu.Lock()
	defer mu.Unlock()
	if cfg == nil {
		level, path = LevelOff, ""
		return
	}
	level = ParseLevel(cfg.LogLevel)
	// PathFor, so Setup and every path-reporting caller (doctor, logs,
	// uninstall --purge) resolve LOG_FILE the same way, including its refusal
	// of a relative value.
	path = PathFor(cfg.LogFile)
}

// Path returns the log file in force.
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	if path != "" {
		return path
	}
	return DefaultPath()
}

// Enabled reports whether anything is being written.
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return level != LevelOff
}

// LevelName is the current level as it appears in the config file.
func LevelName() string {
	mu.Lock()
	defer mu.Unlock()
	return Levels[level]
}

func write(l Level, tag, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if level == LevelOff || l > level || path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	rotate()
	// 0600: the log can hold an address or a config path, so it is no more
	// readable than the session file.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	msg := oneLine(strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
	fmt.Fprintf(f, "%s %-5s %s\n", time.Now().Format("2006-01-02 15:04:05"), tag, msg)
}

// oneLine spells out whatever would end the line or act on the terminal that
// shows it. A logged value is not always this program's own text: an error
// quoted from a server, git's stderr and an address typed into a form all end
// up here. Left raw, a newline forges a second line with a timestamp and a
// level of the sender's choosing, a carriage return overwrites the line
// before it, and an escape sequence is printed to the terminal by
// `tuta-alias logs` (OSC 52 writes the clipboard). The escape is written out
// rather than dropped, so the line still shows that something was there. A
// byte that is not valid UTF-8 is escaped too: a terminal in 8-bit mode reads
// a stray 0x9b as CSI.
func oneLine(s string) string {
	if !strings.ContainsFunc(s, mustEscape) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, `\x%02x`, r)
		case r == '\u2028' || r == '\u2029':
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

// mustEscape is the cheap first pass of oneLine, so that the usual line, which
// has nothing to escape, is not rebuilt. It may say yes to a valid U+FFFD,
// which the second pass then writes back unchanged.
func mustEscape(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == utf8.RuneError
}

// rotate keeps one old generation. Called with mu held.
func rotate() {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < maxBytes {
		return
	}
	_ = os.Rename(path, path+".1")
}

// Errorf logs something that went wrong.
func Errorf(format string, args ...any) { write(LevelError, "ERROR", format, args...) }

// Infof logs something that happened.
func Infof(format string, args ...any) { write(LevelInfo, "INFO", format, args...) }

// Debugf logs detail only useful when chasing a bug.
func Debugf(format string, args ...any) { write(LevelDebug, "DEBUG", format, args...) }

// Tail returns the last n lines of the log, oldest first.
func Tail(n int) ([]string, error) {
	p := Path()
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	// What is in the file was not necessarily written by oneLine: an older
	// version, or another program sharing LOG_FILE, wrote it raw, and `logs`
	// and the Logs screen print what comes back from here.
	for i, l := range lines {
		lines[i] = oneLine(l)
	}
	return lines, nil
}

// Size returns the log's size in bytes, or -1 when there is no log yet.
func Size() int64 { return SizeOf(Path()) }

// SizeOf is Size for a path that is not (yet) the configured one, so a screen
// can show the size of the log a config value points at without calling
// Setup from inside a render.
func SizeOf(path string) int64 {
	if path == "" {
		return -1
	}
	fi, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return fi.Size()
}

// PathFor returns the log file a config would use: LOG_FILE, else the
// default. It reads nothing and changes nothing.
func PathFor(logFile string) string {
	// Absolute only, for the same reason as config.BinaryPath: a hand-edited
	// LOG_FILE="~/tuta.log" created a directory literally named "~" in
	// whatever directory the command happened to run in.
	if p := strings.TrimSpace(logFile); filepath.IsAbs(p) {
		return p
	}
	return DefaultPath()
}

// Clear empties the log (and drops the rotated generation).
func Clear() error {
	p := Path()
	if p == "" {
		// Nothing to clear. p + ".1" would be a file called ".1" in the
		// working directory.
		return nil
	}
	_ = os.Remove(p + ".1")
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
