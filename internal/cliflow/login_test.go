package cliflow

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

// pipeStdin returns a non-terminal *os.File that yields input then EOF.
func pipeStdin(t *testing.T, input string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if _, err := io.WriteString(w, input); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return r
}

// Piped answers for several prompts must all arrive: one shared reader.
func TestLinePrompter_SharedReaderAcrossPrompts(t *testing.T) {
	var out bytes.Buffer
	p := newLinePrompter(&out, pipeStdin(t, "u@tuta.com\n  secret pw \n123456\n"))
	email, err := p.line("Email: ")
	if err != nil || email != "u@tuta.com" {
		t.Fatalf("email = %q, %v", email, err)
	}
	pw, err := p.password("Password: ")
	if err != nil || pw != "  secret pw " {
		t.Fatalf("password = %q, %v (spaces must be kept)", pw, err)
	}
	code, err := p.totp()
	if err != nil || code != 123456 {
		t.Fatalf("totp = %d, %v", code, err)
	}
	if got := out.String(); got != "Email: Password: TOTP code: " {
		t.Errorf("prompts on stdout = %q", got)
	}
}

func TestLinePrompter_Errors(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		ask     func(p *linePrompter) error
		wantIs  error
		wantMsg string
	}{
		{name: "EOF at line", input: "", ask: func(p *linePrompter) error { _, err := p.line("Email: "); return err }, wantIs: errAborted},
		{name: "EOF at password", input: "", ask: func(p *linePrompter) error { _, err := p.password("Password: "); return err }, wantIs: errAborted},
		{name: "last line without newline", input: "u@tuta.com", ask: func(p *linePrompter) error {
			got, err := p.line("Email: ")
			if err == nil && got != "u@tuta.com" {
				return errors.New("got " + got)
			}
			return err
		}},
		{name: "TOTP not digits", input: "12ab56\n", ask: func(p *linePrompter) error { _, err := p.totp(); return err }, wantIs: strconv.ErrSyntax, wantMsg: "TOTP must be digits"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.ask(newLinePrompter(io.Discard, pipeStdin(t, c.input)))
			if c.wantIs == nil {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				return
			}
			if !errors.Is(err, c.wantIs) {
				t.Fatalf("err = %v, want %v", err, c.wantIs)
			}
			if c.wantMsg != "" && !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("err = %q, want containing %q", err, c.wantMsg)
			}
		})
	}
}

// A cancelled prompt exits 130 with "aborted" on stderr instead of "EOF".
func TestLogin_EOFAtPromptAborts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TUTA_EMAIL", "")
	t.Setenv("TUTA_PASSWORD", "")
	cases := []struct {
		name  string
		input string
	}{
		{"at email", ""},
		{"at password", "u@tuta.com\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			orig := os.Stdin
			os.Stdin = pipeStdin(t, c.input)
			t.Cleanup(func() { os.Stdin = orig })
			var out, errOut bytes.Buffer
			code := Login(&out, &errOut, LoginOpts{})
			if code != exitAborted || strings.TrimSpace(errOut.String()) != "aborted" {
				t.Errorf("exit %d, stderr %q; want 130 and \"aborted\"", code, errOut.String())
			}
		})
	}
}

// A network error that wraps io.EOF is not a user abort.
func TestLoginFailed_OnlyPromptAbortIs130(t *testing.T) {
	var errOut bytes.Buffer
	if code := loginFailed(&errOut, io.EOF); code != 2 {
		t.Errorf("io.EOF from the network: exit %d, want 2", code)
	}
	errOut.Reset()
	if code := loginFailed(&errOut, errAborted); code != exitAborted || !strings.Contains(errOut.String(), "aborted") {
		t.Errorf("errAborted: exit %d, stderr %q", code, errOut.String())
	}
}
