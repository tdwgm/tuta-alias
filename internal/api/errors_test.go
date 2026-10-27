package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

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

// hostileText is what a server can put in the text of an error, and how it has
// to read once escaped. They are the cases of logx's TestLogLineCannotBeForged,
// because oneLine is meant to escape the way logx's does.
var hostileText = []struct{ name, value, visible string }{
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
}

// Error is what the CLI prints to stderr and the TUI puts in its error line.
// Code is the Error header and Message the quoted body (or the error-id
// header), and none of that is this program's own text.
func TestAPIError_ErrorEscapesWhatTheServerSent(t *testing.T) {
	for _, tc := range hostileText {
		t.Run(tc.name, func(t *testing.T) {
			got := (&APIError{Status: 500, Code: tc.value, Message: tc.value}).Error()
			if c := controlIn(got); c != -1 {
				t.Errorf("Error() holds the control character %U: %q", c, got)
			}
			if n := strings.Count(got, tc.visible); n != 2 {
				t.Errorf("the code and the message should both still read as %q, found %d: %q", tc.visible, n, got)
			}
		})
	}
}

func TestIsSessionExpired(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&APIError{Status: 440}, true},
		{&APIError{Status: 401}, true},
		{&APIError{Status: 500}, false},
		{&APIError{Status: 404}, false},
		{fmt.Errorf("wrapped: %w", &APIError{Status: 440}), true},
		{errors.New("plain error"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := IsSessionExpired(c.err); got != c.want {
			t.Errorf("IsSessionExpired(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// The body of an error response is quoted into the message, and a terminal
// runs an escape sequence instead of showing it. redact used to mask the
// secrets and keep every other character, control characters included.
func TestRedact_EscapesWhatATerminalWouldRun(t *testing.T) {
	for _, tc := range hostileText {
		t.Run(tc.name, func(t *testing.T) {
			got := redact([]byte(tc.value), 200)
			if c := controlIn(got); c != -1 {
				t.Errorf("redact holds the control character %U: %q", c, got)
			}
			if !strings.Contains(got, tc.visible) {
				t.Errorf("the escaped value should still be readable as %q: %q", tc.visible, got)
			}
		})
	}
}

// max is how much of the body is quoted. Escaping makes that text longer, so
// the cut is made first: a cut made after it could end inside an escape, and a
// body of control characters would crowd out the part that can be read.
func TestRedact_CutsTheBodyBeforeEscapingIt(t *testing.T) {
	got := redact([]byte(strings.Repeat("\x1b", 300)), 200)
	if want := strings.Repeat(`\x1b`, 200); got != want {
		t.Errorf("redact(300 ESC, 200) is %d bytes holding %d escapes, want the first 200 bytes of the body escaped (%d bytes)",
			len(got), strings.Count(got, `\x1b`), len(want))
	}
}

// A cut through a multi-byte character leaves a fragment, which the escaping
// would show as an invalid byte that the server never sent.
func TestRedact_DoesNotCutThroughACharacter(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		max  int
		want string
	}{
		{"a two-byte character cut in half", "aaaa\u00e4X", 5, "aaaa"},
		{"a four-byte character cut after its first byte", "aaaa\U0001F600X", 5, "aaaa"},
		{"a four-byte character cut before its last byte", "aaaa\U0001F600X", 7, "aaaa"},
		{"a character that ends exactly at the cut", "aaaa\u00e4X", 6, "aaaa\u00e4"},
		{"bytes that are not UTF-8 are not looked back through", "\x80\x80\x80\x80\x80\x80\x80\x80", 5, `\x80\x80`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := redact([]byte(tc.body), tc.max); got != tc.want {
				t.Errorf("redact(%q, %d) = %q, want %q", tc.body, tc.max, got, tc.want)
			}
		})
	}
}

// The secrets are masked before the escaping. Escaped first, the newline
// between a key and its value is a backslash and an "n", which the pattern does
// not read as whitespace, and a pretty-printed reply that echoes the request
// would give the password away.
func TestRedact_MasksASecretSeparatedByANewline(t *testing.T) {
	got := redact([]byte("{\n  \"password\":\n    \"hunter2hunter2\"\n}"), 200)
	if strings.Contains(got, "hunter2") {
		t.Errorf("the secret survived: %q", got)
	}
	if !strings.Contains(got, "password=<redacted>") {
		t.Errorf("the mask is missing: %q", got)
	}
}

// Text that is already printable must come out exactly as it went in, or every
// message would change.
func TestRedact_KeepsPrintableText(t *testing.T) {
	const msg = "t\u00e4st-\u00e5\u00e4\u00f6@example.com in C:\\Users\\x\\file.txt: \"quoted\" \\n is two characters here \U0001F600 and a real \uFFFD"
	if got := redact([]byte(msg), 1000); got != msg {
		t.Errorf("printable text was changed:\n got  %q\n want %q", got, msg)
	}
}
