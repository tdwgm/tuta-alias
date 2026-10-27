// Package update is the self-update engine.
//
// There is no release pipeline and the repository is private, so there is no
// artifact to download and no token this program should hold. What a user's
// machine does have is git credentials for the repository and, usually, a Go
// toolchain. The engine therefore leans on both:
//
//   - Check asks the remote which commit a ref points at with `git ls-remote`,
//     which authenticates with the user's own git configuration. This package
//     never sees, stores or passes a credential.
//   - Install runs `go install <module>/cmd/tuta-alias@<ref>` and copies the
//     result over the installed binary.
//
// Nothing here prompts, reads stdin, or writes to stdout/stderr on its own: the
// caller passes the io.Writer that go's output is streamed to. No goroutine
// outlives a call.
// Rebuilding is what a PRIVATE repository with no releases allows. When the
// repository is public and tagged, a release artifact is the better source:
// it needs no Go toolchain, no git credentials and no minutes of compiling.
// The shape here is ready for that: Check() already resolves a ref to a
// commit, so a release path would replace Install() alone and leave Check(),
// Cached(), CheckCached() and Available() as they are.
package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/tdwgm/tuta-alias/internal/atomicfile"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/consts"
)

const (
	defaultRef      = "main"
	defaultInterval = 24 * time.Hour
	binaryName      = "tuta-alias"

	// noLocalCommit is Status.Err for a build that does not know its commit.
	noLocalCommit = "this build does not record the commit it was built from, so it cannot tell whether it is out of date"
)

// Status is the result of one check.
type Status struct {
	Ref       string    `json:"ref"`        // the ref that was checked
	Local     string    `json:"local"`      // the commit this binary was built from ("" when unknown)
	Remote    string    `json:"remote"`     // the commit the ref points at now
	Newer     bool      `json:"newer"`      // a different remote commit AND a known local one
	CheckedAt time.Time `json:"checked_at"` //
	Err       string    `json:"err"`        // why the check could not be made, "" on success
}

// Timeouts. Variables only so a test can shorten them.
var (
	checkTimeout   = 20 * time.Second
	installTimeout = 10 * time.Minute
)

// Seams. The two external commands, the clock, the local commit and the
// install path are package-level variables so the tests never touch the
// network, a real git or a real go, and never depend on how the test binary
// itself was built.
var (
	// runGit runs git with args and returns its stdout. On failure the error
	// carries git's stderr.
	runGit = func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		// Never ask for a password on a terminal that is not ours: with this
		// set git fails with "could not read Username" instead of hanging.
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		// Not the caller's directory. git reads .git/config of the repository
		// it is started in, and one that arrived as an archive can set
		// url.<base>.insteadOf to send this check to another server, and
		// core.sshCommand or credential.helper to run a command of its own.
		// The check asks about this program's repository, not the one the user
		// happens to be standing in. runGo below is moved for the same reason.
		cmd.Dir = os.TempDir()
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		cmd.WaitDelay = 2 * time.Second
		if err := cmd.Run(); err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, fmt.Errorf("%s", msg)
			}
			return nil, err
		}
		return stdout.Bytes(), nil
	}

	// runGo runs the go command with env added to the process environment and
	// streams its combined stdout+stderr to out as it is produced.
	runGo = func(ctx context.Context, env []string, out io.Writer, args ...string) error {
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Env = append(os.Environ(), env...)
		cmd.Dir = os.TempDir() // the module in the cwd must not matter
		// The same writer for both streams: exec then uses one pipe, so the
		// two come out in the order go wrote them.
		cmd.Stdout, cmd.Stderr = out, out
		// go spawns compilers; without this a killed go whose children keep
		// the pipe open would hang Wait.
		cmd.WaitDelay = 5 * time.Second
		return cmd.Run()
	}

	lookPath = exec.LookPath
	now      = time.Now

	// localCommit is the commit this binary was built from, "" when unknown.
	localCommit = func() string { return strings.TrimSpace(consts.CommitHash()) }

	// installPath is where the binary is (or will be) installed.
	installPath = func() string {
		if cfg, err := config.Load(); err == nil && cfg != nil {
			return cfg.BinaryPath()
		}
		return config.DefaultInstallPath()
	}
)

// CachePath is where the last check is stored:
// $XDG_CACHE_HOME/tuta-alias/update-check, else ~/.cache/tuta-alias/update-check.
// It is "" when neither can be determined.
func CachePath() string {
	base := os.Getenv("XDG_CACHE_HOME")
	// The XDG spec says a relative value is invalid and must be ignored.
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "tuta-alias", "update-check")
}

// Check asks the remote which commit ref points at, with one `git ls-remote`.
// On success the result is also written to the cache (best effort). On failure
// the returned Status carries the same text in Err and the cache is left alone.
func Check(ref string) (Status, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = defaultRef
	}
	st := Status{Ref: ref, Local: localCommit(), CheckedAt: now()}
	fail := func(err error) (Status, error) {
		st.Err = err.Error()
		return st, err
	}

	if err := validRef(ref); err != nil {
		return fail(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	// The ref is one argument of an exec, never part of a shell line. The
	// second pattern is for an annotated tag: a pattern has to match the end
	// of the name it lists, so "v1" lists the tag object but never the
	// "refs/tags/v1^{}" line, which is the commit.
	out, err := runGit(ctx, "ls-remote", consts.RepoURL, ref, ref+"^{}")
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fail(fmt.Errorf("git ls-remote timed out after %s (is the network reachable?)", checkTimeout))
		}
		return fail(fmt.Errorf("git ls-remote %s: %s", ref, describeGitError(err)))
	}
	sha := parseLsRemote(out, ref)
	if sha == "" {
		return fail(fmt.Errorf("ref %q does not exist in %s", ref, consts.RepoURL))
	}
	if n := len(st.Local); n > 0 && n < len(sha) {
		sha = sha[:n] // short to short
	}
	st.Remote = sha
	reconcile(&st)
	writeCache(st)
	return st, nil
}

// Cached returns the last check from disk. A missing, unreadable or corrupt
// cache is simply "no cache".
//
// Local and Newer are recomputed against the binary that is running now, so a
// binary that was just updated does not keep reporting the update it installed.
func Cached() (Status, bool) {
	p := CachePath()
	if p == "" {
		return Status{}, false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return Status{}, false
	}
	var st Status
	if json.Unmarshal(raw, &st) != nil || st.Remote == "" || st.CheckedAt.IsZero() {
		return Status{}, false
	}
	reconcile(&st)
	return st, true
}

// CheckCached is Check, but only when the cached check is older than interval
// (a Go duration; unparsable or negative falls back to 24h) or was made for a
// different ref. Otherwise it returns the cached one without touching the
// network.
func CheckCached(ref, interval string) (Status, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = defaultRef
	}
	if st, ok := Cached(); ok && st.Ref == ref {
		if age := now().Sub(st.CheckedAt); age >= 0 && age < parseInterval(interval) {
			return st, nil
		}
	}
	return Check(ref)
}

// Install builds ref with `go install` and puts the binary at dst.
//
// go's combined output is streamed to out while it runs, because a build can
// take a minute and silence looks like a hang. dst is replaced atomically and
// is not touched at all unless the build succeeded.
func Install(ref string, dst string, out io.Writer) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = defaultRef
	}
	if err := validRef(ref); err != nil {
		return err
	}
	if dst == "" {
		return errors.New("no install path to put the updated binary at")
	}
	if out == nil {
		out = io.Discard
	}

	// Build into a private GOBIN rather than looking for it in the user's own
	// $GOBIN or $GOPATH/bin: that way the result is found deterministically,
	// a tuta-alias the user keeps in ~/go/bin is not overwritten behind their
	// back, and there is no `go env` round trip.
	work, err := os.MkdirTemp("", "tuta-alias-update-*")
	if err != nil {
		return fmt.Errorf("create build directory: %w", err)
	}
	defer os.RemoveAll(work)

	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	pkg := consts.ModulePath + "/cmd/" + binaryName + "@" + ref
	env := []string{
		// The module is private: do not ask the public proxy or the public
		// checksum database about it. GOPRIVATE is also the default for
		// GONOPROXY and GONOSUMDB, so those are not set separately.
		"GOPRIVATE=" + withPrivate(os.Getenv("GOPRIVATE")),
		"GOBIN=" + work,
	}
	if err := runGo(ctx, env, out, "install", pkg); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("go install %s timed out after %s", pkg, installTimeout)
		}
		return fmt.Errorf("go install %s: %w", pkg, err)
	}

	built := filepath.Join(work, binaryName)
	if runtime.GOOS == "windows" {
		built += ".exe"
	}
	data, err := os.ReadFile(built)
	if err != nil {
		return fmt.Errorf("go install succeeded but produced no %s binary: %w", binaryName, err)
	}
	if len(data) == 0 {
		return fmt.Errorf("go install produced an empty %s binary", binaryName)
	}
	// A temp file in dst's directory, chmod, rename: replacing a running
	// binary in place fails with ETXTBSY and a crash would leave half of one.
	if err := atomicfile.WriteMkdir(dst, data, 0o755, 0o755); err != nil {
		return fmt.Errorf("install %s: %w", dst, err)
	}
	return nil
}

// Available is nil when an update can be done here: git and go on PATH and the
// install location writable. Otherwise one error names everything that is
// missing.
func Available() error {
	var missing []string
	if _, err := lookPath("git"); err != nil {
		missing = append(missing, "git is not on PATH (needed to ask which commit is newest)")
	}
	if _, err := lookPath("go"); err != nil {
		missing = append(missing, "go is not on PATH (needed to build the update)")
	}
	dst := installPath()
	switch {
	case dst == "":
		missing = append(missing, "no install path is configured")
	default:
		if err := writableDir(filepath.Dir(dst)); err != nil {
			missing = append(missing, fmt.Sprintf("cannot write to %s: %v", filepath.Dir(dst), err))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return errors.New("cannot update: " + strings.Join(missing, "; "))
}

// ---- helpers ---------------------------------------------------------------

// validRef refuses a ref that could be read as an option or that cannot be a
// ref at all. exec already keeps it out of any shell.
func validRef(ref string) error {
	if len(ref) > 255 || strings.HasPrefix(ref, "-") ||
		strings.IndexFunc(ref, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return fmt.Errorf("invalid update ref %q", ref)
	}
	return nil
}

// parseLsRemote returns the commit that ref names in the output of
// `git ls-remote <url> <ref> <ref>^{}`, or "" when nothing in it is named ref.
//
// The first line is not the answer. git takes ref as a pattern that matches the
// end of a ref name, so "main" also lists refs/heads/feature/main, and it lists
// in name order, which puts that branch before refs/heads/main. Only a name
// that is exactly ref counts here. A tag beats a branch of the same name
// because that is the order `go install <module>@<ref>` resolves a name in,
// and what the check compares has to be what Install builds. HEAD, or a ref
// written out in full, is taken as written.
//
// An annotated tag is listed twice: as the tag object and, on a "<name>^{}"
// line, as the commit it points at. The commit is what a build resolves to, so
// that line wins when it is there.
func parseLsRemote(out []byte, ref string) string {
	shas := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !isHex(f[0]) {
			continue
		}
		shas[f[1]] = strings.ToLower(f[0])
	}
	for _, name := range []string{"refs/tags/" + ref, "refs/heads/" + ref, ref} {
		if sha := shas[name+"^{}"]; sha != "" {
			return sha
		}
		if sha := shas[name]; sha != "" {
			return sha
		}
	}
	return ""
}

func isHex(s string) bool {
	if len(s) < 7 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// reconcile sets Local, Newer and Err from st.Remote and the running binary.
func reconcile(st *Status) {
	st.Local = localCommit()
	st.Err = ""
	st.Newer = false
	if st.Local == "" {
		st.Err = noLocalCommit
		return
	}
	st.Newer = st.Remote != "" && !sameCommit(st.Local, st.Remote)
}

// sameCommit compares two possibly differently abbreviated commits.
func sameCommit(a, b string) bool {
	n := min(len(a), len(b))
	return n > 0 && strings.EqualFold(a[:n], b[:n])
}

func parseInterval(s string) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil || d < 0 {
		return defaultInterval
	}
	return d
}

// writeCache is best effort: a check that cannot be remembered is still a check.
func writeCache(st Status) {
	p := CachePath()
	if p == "" {
		return
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = atomicfile.WriteMkdir(p, append(data, '\n'), 0o600, 0o700)
}

// credInURL matches the userinfo of a URL ("https://user:secret@host").
var credInURL = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s@]+@`)

// describeGitError makes git's failure fit on one line and removes anything
// that looks like a credential. git can echo a remote URL it was configured
// with (url.<base>.insteadOf, a credential-bearing remote), and this text ends
// up on screen and in logs.
func describeGitError(err error) string {
	msg := credInURL.ReplaceAllString(err.Error(), "${1}")
	msg = strings.ReplaceAll(msg, "\n", "; ")
	// git relays what the remote and the helpers it starts wrote (ssh, a
	// credential helper, a proxy), and this text is printed on a terminal and
	// written to the log. A control character in it is run by the terminal
	// rather than shown: OSC 52 sets the clipboard, CSI clears the screen. Each
	// becomes a space here, which the Fields below then collapse. Map also
	// replaces a byte that is not valid UTF-8, and a stray 0x9b is CSI to a
	// terminal in 8-bit mode.
	msg = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, msg)
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	low := strings.ToLower(msg)
	if strings.Contains(low, "could not read username") ||
		strings.Contains(low, "authentication failed") ||
		strings.Contains(low, "repository not found") {
		msg += " (git has no working credentials for this repository)"
	}
	return msg
}

// withPrivate adds the module path to a GOPRIVATE list without dropping what
// the user already had in it.
func withPrivate(existing string) string {
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return consts.ModulePath
	}
	for _, p := range strings.Split(existing, ",") {
		if strings.TrimSpace(p) == consts.ModulePath {
			return existing
		}
	}
	return existing + "," + consts.ModulePath
}

// writableDir reports whether a file could be created in dir, or in the
// nearest existing ancestor when dir does not exist yet (Install creates it).
// It tries to create a file instead of reading permission bits, so read-only
// mounts, ACLs and root all give the right answer.
func writableDir(dir string) error {
	for {
		fi, err := os.Stat(dir)
		if err == nil {
			if !fi.IsDir() {
				return fmt.Errorf("%s is not a directory", dir)
			}
			f, err := os.CreateTemp(dir, ".tuta-alias-probe-*")
			if err != nil {
				return err
			}
			name := f.Name()
			_ = f.Close()
			return os.Remove(name)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return err
		}
		dir = parent
	}
}
