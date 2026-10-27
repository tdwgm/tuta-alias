package cliflow

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/creds"
	"github.com/tdwgm/tuta-alias/internal/login"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// LoginOpts carries flags for the login command.
type LoginOpts struct {
	Email   string
	TOTP    int
	Verbose bool
	Save    bool // store email + password in the system keyring after success
	Saved   bool // take email + password from the keyring login saved earlier
}

func vlog(stderr io.Writer, verbose bool, msg string) {
	if verbose {
		fmt.Fprintf(stderr, "→ %s\n", msg)
	}
}

// errAborted is returned by the login prompts when the user cancels (Ctrl+C,
// Ctrl+D) or stdin ends. It is distinct from io.EOF so a connection closed by
// the server is never mistaken for a user abort.
var errAborted = errors.New("aborted")

// exitAborted is the exit code for a cancelled login (128 + SIGINT).
const exitAborted = 130

// linePrompter asks the login questions. When stdin is not a terminal, one
// bufio.Reader is shared by every prompt of the login: a reader per prompt
// would swallow input buffered ahead (e.g. the TOTP line after the email).
type linePrompter struct {
	stdout io.Writer
	in     *os.File
	br     *bufio.Reader
}

func newLinePrompter(stdout io.Writer, in *os.File) *linePrompter {
	return &linePrompter{stdout: stdout, in: in, br: bufio.NewReader(in)}
}

// line prompts for one line. On a terminal it edits the line itself in raw
// mode instead of relying on the kernel: erase is only handled in canonical
// mode, so a tty inherited in -icanon echoed Backspace as "^?". Ctrl+C,
// Ctrl+D and end of input return errAborted.
func (p *linePrompter) line(prompt string) (string, error) {
	fd := int(p.in.Fd())
	if !term.IsTerminal(fd) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprint(p.stdout, prompt)
		line, err := p.buffered()
		return strings.TrimSpace(line), err
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return "", fmt.Errorf("terminal raw mode: %w", err)
	}
	defer func() { _ = term.Restore(fd, old) }()
	t := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{p.in, p.stdout}, prompt)
	line, err := t.ReadLine()
	if errors.Is(err, io.EOF) {
		return "", errAborted
	}
	if err != nil {
		return "", fmt.Errorf("read line: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// password prompts without echo on a terminal. When stdin is not a terminal
// it reads one line from the shared reader, keeping inner/outer spaces (only
// the line ending is dropped).
func (p *linePrompter) password(prompt string) (string, error) {
	fd := int(p.in.Fd())
	fmt.Fprint(p.stdout, prompt)
	if !term.IsTerminal(fd) {
		return p.buffered()
	}
	bs, err := term.ReadPassword(fd)
	fmt.Fprintln(p.stdout)
	if errors.Is(err, io.EOF) {
		return "", errAborted
	}
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(bs), nil
}

// totp prompts for the TOTP code.
func (p *linePrompter) totp() (int, error) {
	line, err := p.line("TOTP code: ")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(line)
	if err != nil {
		return 0, fmt.Errorf("TOTP must be digits: %w", err)
	}
	return n, nil
}

// buffered reads one line from the shared reader without its line ending.
// A final line without a newline is accepted; no input at all is errAborted.
func (p *linePrompter) buffered() (string, error) {
	line, err := p.br.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		if errors.Is(err, io.EOF) {
			return "", errAborted
		}
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// loginFailed reports a login error: a cancelled prompt prints "aborted" and
// exits 130, anything else goes through bail.
func loginFailed(stderr io.Writer, err error) int {
	if errors.Is(err, errAborted) {
		fmt.Fprintln(stderr, "aborted")
		return exitAborted
	}
	return bail(stderr, err)
}

// Login performs interactive login. Mirrors tuta_alias/cli.py::login + _do_login_verbose.
func Login(stdout, stderr io.Writer, opts LoginOpts) int {
	email := opts.Email
	savedPW := ""
	if opts.Saved {
		c, err := creds.LoadLogin()
		if err != nil {
			return bail(stderr, err)
		}
		if email == "" {
			email = c.Email
		}
		if strings.EqualFold(email, c.Email) {
			savedPW = c.Password
		}
		vlog(stderr, opts.Verbose, "using login saved in the system keyring")
	}
	if email == "" {
		email = os.Getenv("TUTA_EMAIL")
	}
	ask := newLinePrompter(stdout, os.Stdin)
	if email == "" {
		line, err := ask.line("Email: ")
		if err != nil {
			return loginFailed(stderr, err)
		}
		email = line
	}
	pw := savedPW
	if pw == "" {
		pw = os.Getenv("TUTA_PASSWORD")
	}
	if pw == "" {
		line, err := ask.password("Password: ")
		if err != nil {
			return loginFailed(stderr, err)
		}
		pw = line
	}
	totp := opts.TOTP
	if totp == 0 {
		if e := os.Getenv("TUTA_TOTP"); e != "" {
			n, _ := strconv.Atoi(e)
			totp = n
		}
	}
	vlog(stderr, opts.Verbose, fmt.Sprintf("fetch_salt(%s)", email))
	vlog(stderr, opts.Verbose, "deriving Argon2id key (this takes ~2s)…")
	c := api.NewClient(consts.BaseURL, "")
	sess, err := login.Login(c, email, pw, totp, ask.totp)
	if err != nil {
		if errors.Is(err, login.ErrTOTPRequired) {
			fmt.Fprintln(stderr, "2FA required: account has TOTP enabled; supply --totp")
			return 2
		}
		// bail maps 401 to "Session expired", which is wrong while logging in.
		var ae *api.APIError
		if errors.As(err, &ae) && ae.Status == 401 {
			fmt.Fprintf(stderr, "Login rejected (%v) — check email, password and TOTP.\n", err)
			return 2
		}
		return loginFailed(stderr, err)
	}
	vlog(stderr, opts.Verbose, "resolve_account_ids OK")
	p, err := login.SaveSession(sess)
	if err != nil {
		return bail(stderr, err)
	}
	vlog(stderr, opts.Verbose, fmt.Sprintf("saved session to %s", p))
	fmt.Fprintf(stdout, "Logged in as %s; session saved to %s\n", sess.MailAddress, p)
	if opts.Save {
		if err := creds.SaveLogin(email, pw); err != nil {
			fmt.Fprintln(stderr, "warning:", err)
		} else {
			fmt.Fprintln(stdout, "Login saved to the system keyring (TOTP is never saved).")
		}
	}
	return 0
}

// Logout closes the session on the server and deletes the session file.
//
// A session file that could not be deleted is a failure, not a warning: the
// access token is still on disk and the next command logs back in with it.
// This used to print "No session to clear." and exit 0 in exactly that case.
func Logout(stdout io.Writer) int {
	cleared, err := login.Logout()
	if errors.Is(err, login.ErrSessionFileRemains) {
		fmt.Fprintln(stdout, "error:", err)
		fmt.Fprintf(stdout, "The access token is still in %s. Remove it by hand.\n",
			config.ShortPath(session.Path()))
		return 1
	}
	if err != nil {
		// Only the server close is left, and that is allowed to fail: the
		// token expires on its own and the local file is gone.
		fmt.Fprintln(stdout, "warning: could not close the session on the server:", err)
	}
	if cleared {
		fmt.Fprintln(stdout, "Session cleared.")
	} else {
		fmt.Fprintln(stdout, "No session to clear.")
	}
	return 0
}
