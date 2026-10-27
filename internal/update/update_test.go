package update

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tdwgm/tuta-alias/internal/consts"
)

const (
	fullSHA  = "0123456789abcdef0123456789abcdef01234567"
	shortSHA = "0123456"
	otherSHA = "fedcba9876543210fedcba9876543210fedcba98"
)

// sandbox points HOME, the cache and the temp dir at t.TempDir() and swaps the
// seams for harmless defaults; every test then overrides what it exercises.
// The originals are restored by t.Cleanup.
func sandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	t.Setenv("GOPRIVATE", "")
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}

	oGit, oGo, oLook, oNow, oLocal, oPath := runGit, runGo, lookPath, now, localCommit, installPath
	t.Cleanup(func() {
		runGit, runGo, lookPath, now, localCommit, installPath = oGit, oGo, oLook, oNow, oLocal, oPath
	})
	runGit = func(context.Context, ...string) ([]byte, error) {
		t.Error("unexpected git invocation")
		return nil, errors.New("unexpected git invocation")
	}
	runGo = func(context.Context, []string, io.Writer, ...string) error {
		t.Error("unexpected go invocation")
		return errors.New("unexpected go invocation")
	}
	lookPath = func(string) (string, error) { return "/usr/bin/fake", nil }
	localCommit = func() string { return shortSHA }
	installPath = func() string { return filepath.Join(root, "bin", "tuta-alias") }
	return root
}

// fakeGit makes runGit answer with out/err and counts the calls.
func fakeGit(out string, err error) *int {
	calls := new(int)
	runGit = func(ctx context.Context, args ...string) ([]byte, error) {
		*calls++
		return []byte(out), err
	}
	return calls
}

func TestCheckParsesLsRemote(t *testing.T) {
	sandbox(t)
	var gotArgs []string
	runGit = func(ctx context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		if _, ok := ctx.Deadline(); !ok {
			t.Error("git was run without a timeout")
		}
		return []byte(fullSHA + "\trefs/heads/main\n"), nil
	}
	st, err := Check("main")
	if err != nil {
		t.Fatal(err)
	}
	if st.Remote != shortSHA {
		t.Errorf("Remote = %q, want the short sha %q", st.Remote, shortSHA)
	}
	if st.Ref != "main" || st.Local != shortSHA || st.Err != "" || st.CheckedAt.IsZero() {
		t.Errorf("unexpected status %+v", st)
	}
	want := []string{"ls-remote", consts.RepoURL, "main", "main^{}"}
	if strings.Join(gotArgs, "|") != strings.Join(want, "|") {
		t.Errorf("git args = %q, want %q", gotArgs, want)
	}
}

func TestCheckPrefersPeeledTagCommit(t *testing.T) {
	sandbox(t)
	tag := "1111111111111111111111111111111111111111"
	fakeGit(tag+"\trefs/tags/v1\n"+fullSHA+"\trefs/tags/v1^{}\n", nil)
	st, err := Check("v1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Remote != shortSHA {
		t.Errorf("Remote = %q, want the commit the tag points at (%q)", st.Remote, shortSHA)
	}
}

// commitID is a commit id made of one digit repeated, so that a case can say
// which of several refs an answer came from.
func commitID(digit byte) string { return strings.Repeat(string(digit), 40) }

// git takes the ref as a pattern that matches the end of a ref name and lists
// in name order, so asking for "main" also lists refs/heads/feature/main, and
// that line comes first. The check used to take the first line as its answer:
// it compared the running build with a branch nobody asked about, and reported
// a different build when there was none, or none when there was one.
func TestCheckAnswersForTheRefAskedNotForALookalike(t *testing.T) {
	a, b, c, d := commitID('a'), commitID('b'), commitID('c'), commitID('d')
	cases := []struct {
		name string
		ref  string
		out  string // what git prints
		want string // the commit the ref names, "" when it names nothing
	}{
		{"a branch listed after branches that end in its name", "main",
			a + "\trefs/heads/feature/main\n" + a + "\trefs/heads/fix/main\n" +
				b + "\trefs/heads/main\n" + a + "\trefs/heads/zz/main\n", b},
		{"a tag listed after a branch that ends in its name", "v1",
			a + "\trefs/heads/release/v1\n" + c + "\trefs/tags/v1\n", c},
		{"an annotated tag is the commit it points at, not the tag object", "v1",
			d + "\trefs/tags/v1\n" + c + "\trefs/tags/v1^{}\n", c},
		{"a tag and a branch of one name: the tag, as go install resolves it", "main",
			b + "\trefs/heads/main\n" + c + "\trefs/tags/main\n", c},
		{"HEAD", "HEAD", b + "\tHEAD\n", b},
		{"a ref written out in full", "refs/heads/main", b + "\trefs/heads/main\n", b},
		{"nothing but lookalikes", "main", a + "\trefs/heads/feature/main\n" + a + "\trefs/tags/old/main\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			fakeGit(tc.out, nil)
			if tc.want == "" {
				if _, err := Check(tc.ref); err == nil || !strings.Contains(err.Error(), tc.ref) {
					t.Errorf("Check(%q) = %v, want an error saying the ref does not exist", tc.ref, err)
				}
				return
			}
			localCommit = func() string { return tc.want[:7] }
			st, err := Check(tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			if st.Remote != tc.want[:7] {
				t.Errorf("Remote = %q, want %q", st.Remote, tc.want[:7])
			}
			if st.Newer {
				t.Errorf("the build the ref points at is reported as a different build: %+v", st)
			}
		})
	}
}

// The cases above only know what the author believed git prints, and that was
// wrong once already: for a plain tag name git lists the tag object and never
// the "^{}" line that has the commit, so an annotated tag (the kind this
// project's own pre-push hook makes) never matched the build installed from it.
// This runs the real ls-remote, with the arguments Check builds, against a
// repository that has the refs which made the first line the wrong answer.
func TestCheckAgainstARealRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	realGit := runGit
	root := sandbox(t)
	// A git that sees only what this test gives it, and may commit without an
	// identity of its own.
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, k := range []string{"GIT_AUTHOR", "GIT_COMMITTER"} {
		t.Setenv(k+"_NAME", "test")
		t.Setenv(k+"_EMAIL", "test@example.com")
	}

	remote := filepath.Join(root, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", remote}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("symbolic-ref", "HEAD", "refs/heads/main")
	git("commit", "-q", "--allow-empty", "-m", "one")
	one := git("rev-parse", "HEAD")
	git("branch", "feature/main") // ends in "main"
	git("branch", "release/v1")   // ends in a tag's name
	git("branch", "hotfix/v3")    // the only thing that ends in "v3"
	git("tag", "v0")              // a lightweight tag
	git("commit", "-q", "--allow-empty", "-m", "two")
	two := git("rev-parse", "HEAD")
	git("tag", "-a", "-m", "v1", "v1")
	git("tag", "-a", "-m", "v2", "v2")
	git("commit", "-q", "--allow-empty", "-m", "three")
	three := git("rev-parse", "HEAD")
	git("branch", "both")
	git("tag", "both", two) // a tag and a branch with one name

	runGit = func(ctx context.Context, args ...string) ([]byte, error) {
		local := make([]string, len(args))
		for i, a := range args {
			local[i] = a
			if a == consts.RepoURL {
				local[i] = remote
			}
		}
		return realGit(ctx, local...)
	}

	for _, tc := range []struct{ name, ref, want string }{
		{"a branch with lookalikes before it", "main", three},
		{"an annotated tag with a lookalike branch before it", "v1", two},
		{"an annotated tag on its own", "v2", two},
		{"a lightweight tag", "v0", one},
		{"a tag and a branch of one name", "both", two},
		{"HEAD", "HEAD", three},
		{"only a lookalike", "v3", ""},
		{"nothing", "nope", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want == "" {
				if _, err := Check(tc.ref); err == nil || !strings.Contains(err.Error(), "does not exist") {
					t.Errorf("Check(%q) = %v, want an error saying the ref does not exist", tc.ref, err)
				}
				return
			}
			localCommit = func() string { return tc.want[:7] }
			st, err := Check(tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			if st.Remote != tc.want[:7] {
				t.Errorf("Remote = %q, want %q", st.Remote, tc.want[:7])
			}
			if st.Newer {
				t.Errorf("the build the ref points at is reported as a different build: %+v", st)
			}
		})
	}
}

func TestCheckEmptyOutputNamesRef(t *testing.T) {
	sandbox(t)
	fakeGit("", nil)
	st, err := Check("no-such-branch")
	if err == nil {
		t.Fatal("want an error for a ref that resolves to nothing")
	}
	if !strings.Contains(err.Error(), "no-such-branch") {
		t.Errorf("error %q does not name the ref", err)
	}
	if st.Err == "" {
		t.Error("Status.Err is empty on a failed check")
	}
	if _, ok := Cached(); ok {
		t.Error("a failed check must not be cached")
	}
}

func TestCheckGitErrorIsReadableAndHasNoCredential(t *testing.T) {
	sandbox(t)
	fakeGit("", errors.New("fatal: could not read Username for 'https://github.com': terminal prompts disabled\n"))
	_, err := Check("main")
	if err == nil {
		t.Fatal("want the git error")
	}
	if !strings.Contains(err.Error(), "could not read Username") {
		t.Errorf("error lost git's reason: %q", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error is not one line: %q", err)
	}

	// A URL with embedded userinfo (insteadOf rewrite, credential-bearing
	// remote) must not leak through the error.
	fakeGit("", errors.New("fatal: unable to access 'https://user:s3cretT0ken@github.com/tdwgm/tuta-alias/': 403")) // gitleaks:allow (a fake credential, which is what the test redacts)
	_, err = Check("main")
	if err == nil {
		t.Fatal("want the git error")
	}
	for _, leak := range []string{"s3cretT0ken", "user:"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error leaks %q: %q", leak, err)
		}
	}
	if !strings.Contains(err.Error(), "github.com") {
		t.Errorf("error should still say where: %q", err)
	}
}

func TestCheckTimeoutIsReported(t *testing.T) {
	sandbox(t)
	old := checkTimeout
	checkTimeout = 20 * time.Millisecond
	t.Cleanup(func() { checkTimeout = old })
	runGit = func(ctx context.Context, args ...string) ([]byte, error) {
		<-ctx.Done() // a hung network
		return nil, errors.New("signal: killed")
	}
	_, err := Check("main")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("want a timeout error, got %v", err)
	}
}

func TestCheckRejectsOptionLookingRef(t *testing.T) {
	sandbox(t) // runGit fails the test if called
	for _, ref := range []string{"--upload-pack=evil", "-x", "a b", "main\n--foo"} {
		if _, err := Check(ref); err == nil {
			t.Errorf("Check(%q) should be refused", ref)
		}
	}
}

func TestNewer(t *testing.T) {
	sandbox(t)

	fakeGit(otherSHA+"\trefs/heads/main\n", nil)
	st, err := Check("main")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Newer {
		t.Errorf("a different remote commit must be Newer: %+v", st)
	}
	if st.Err != "" {
		t.Errorf("Err = %q on a clean check", st.Err)
	}

	fakeGit(fullSHA+"\trefs/heads/main\n", nil)
	if st, _ = Check("main"); st.Newer {
		t.Errorf("the same commit must not be Newer: %+v", st)
	}

	localCommit = func() string { return "" }
	fakeGit(otherSHA+"\trefs/heads/main\n", nil)
	st, err = Check("main")
	if err != nil {
		t.Fatalf("an unknown local commit is not a failed check: %v", err)
	}
	if st.Newer {
		t.Error("a binary that does not know its commit must not claim to be out of date")
	}
	if st.Err == "" {
		t.Error("Err should say why Newer is false")
	}
	if st.Local != "" || st.Remote == "" {
		t.Errorf("unexpected status %+v", st)
	}
}

func TestCached(t *testing.T) {
	sandbox(t)
	if _, ok := Cached(); ok {
		t.Error("Cached on a missing file should be false")
	}

	p := CachePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, junk := range []string{"{not json", "", "null", `{"ref":"main"}`} {
		if err := os.WriteFile(p, []byte(junk), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := Cached(); ok {
			t.Errorf("Cached on %q should be false", junk)
		}
	}

	fakeGit(otherSHA+"\trefs/heads/main\n", nil)
	want, err := Check("main")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := Cached()
	if !ok {
		t.Fatal("Cached is false after a Check that wrote one")
	}
	if got.Ref != want.Ref || got.Remote != want.Remote || got.Local != want.Local ||
		got.Newer != want.Newer || !got.CheckedAt.Equal(want.CheckedAt) {
		t.Errorf("Cached = %+v, want %+v", got, want)
	}
}

func TestCachedFollowsTheRunningBinary(t *testing.T) {
	sandbox(t)
	fakeGit(otherSHA+"\trefs/heads/main\n", nil)
	if st, _ := Check("main"); !st.Newer {
		t.Fatal("setup: want Newer")
	}
	// The binary is replaced by the update and restarted: it now IS the remote.
	localCommit = func() string { return otherSHA[:7] }
	st, ok := Cached()
	if !ok {
		t.Fatal("no cache")
	}
	if st.Newer || st.Local != otherSHA[:7] {
		t.Errorf("a stale cache still reports an update after installing it: %+v", st)
	}
}

func TestCacheFileMode(t *testing.T) {
	sandbox(t)
	fakeGit(fullSHA+"\trefs/heads/main\n", nil)
	if _, err := Check("main"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(CachePath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("cache mode = %v, want 0600", fi.Mode().Perm())
	}
	if !strings.HasSuffix(CachePath(), filepath.Join("tuta-alias", "update-check")) {
		t.Errorf("CachePath = %q", CachePath())
	}
	entries, _ := os.ReadDir(filepath.Dir(CachePath()))
	if len(entries) != 1 {
		t.Errorf("cache dir holds %d entries, want only the cache", len(entries))
	}
}

func TestCachePathFallsBackToHome(t *testing.T) {
	root := sandbox(t)
	t.Setenv("XDG_CACHE_HOME", "")
	want := filepath.Join(root, ".cache", "tuta-alias", "update-check")
	if got := CachePath(); got != want {
		t.Errorf("CachePath = %q, want %q", got, want)
	}
	t.Setenv("XDG_CACHE_HOME", "relative/dir") // invalid per the XDG spec
	if got := CachePath(); got != want {
		t.Errorf("a relative XDG_CACHE_HOME was honoured: %q", got)
	}
}

func TestCheckCachedFreshDoesNotCheck(t *testing.T) {
	sandbox(t)
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return clock }
	calls := fakeGit(otherSHA+"\trefs/heads/main\n", nil)
	if _, err := Check("main"); err != nil {
		t.Fatal(err)
	}
	*calls = 0

	clock = clock.Add(30 * time.Minute)
	st, err := CheckCached("main", "1h")
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 0 {
		t.Errorf("a fresh cache still ran the check (%d calls)", *calls)
	}
	if st.Remote != otherSHA[:7] || !st.Newer {
		t.Errorf("did not return the cached status: %+v", st)
	}
}

func TestCheckCachedRunsWhenStaleOrUnparsable(t *testing.T) {
	sandbox(t)
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return clock }
	// A remote with both branches. It used to answer with main's line whatever
	// was asked for, which only worked while the first line of the output was
	// taken for the answer.
	calls := fakeGit(otherSHA+"\trefs/heads/dev\n"+otherSHA+"\trefs/heads/main\n", nil)

	// No cache at all.
	if _, err := CheckCached("main", "1h"); err != nil || *calls != 1 {
		t.Fatalf("no cache: err=%v calls=%d, want a check", err, *calls)
	}
	// Older than the interval.
	clock = clock.Add(2 * time.Hour)
	if _, err := CheckCached("main", "1h"); err != nil || *calls != 2 {
		t.Fatalf("stale: err=%v calls=%d, want a check", err, *calls)
	}
	// Unparsable interval falls back to 24h: 2h old is fresh, 25h old is not.
	clock = clock.Add(2 * time.Hour)
	for _, bad := range []string{"", "soon", "-5m"} {
		if _, err := CheckCached("main", bad); err != nil || *calls != 2 {
			t.Fatalf("interval %q: err=%v calls=%d, want the 24h default (cache still fresh)", bad, err, *calls)
		}
	}
	clock = clock.Add(25 * time.Hour)
	if _, err := CheckCached("main", "soon"); err != nil || *calls != 3 {
		t.Fatalf("unparsable + 25h old: err=%v calls=%d, want a check", err, *calls)
	}
	// A cache made for another ref does not answer for this one.
	if _, err := CheckCached("dev", "24h"); err != nil || *calls != 4 {
		t.Fatalf("other ref: err=%v calls=%d, want a check", err, *calls)
	}
}

func TestCheckCachedFailureKeepsOldCache(t *testing.T) {
	sandbox(t)
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return clock }
	fakeGit(otherSHA+"\trefs/heads/main\n", nil)
	if _, err := Check("main"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(48 * time.Hour)
	fakeGit("", errors.New("fatal: unable to access: Could not resolve host"))
	if _, err := CheckCached("main", "1h"); err == nil {
		t.Fatal("want the error of the failed check")
	}
	if st, ok := Cached(); !ok || st.Remote != otherSHA[:7] {
		t.Errorf("a failed check destroyed the last good one: %+v ok=%v", st, ok)
	}
}

// fakeGoInstall is a stand-in for `go install`: it prints something, then
// writes the "built" binary into the GOBIN it was given.
func fakeGoInstall(t *testing.T, content string, fail error) (args *[]string, env *[]string) {
	t.Helper()
	args, env = new([]string), new([]string)
	runGo = func(ctx context.Context, e []string, out io.Writer, a ...string) error {
		*args, *env = a, e
		if _, ok := ctx.Deadline(); !ok {
			t.Error("go was run without a timeout")
		}
		io.WriteString(out, "go: downloading github.com/tdwgm/tuta-alias\n")
		io.WriteString(out, "linking\n")
		if fail != nil {
			io.WriteString(out, "build failed\n")
			return fail
		}
		for _, kv := range e {
			if dir, ok := strings.CutPrefix(kv, "GOBIN="); ok {
				return os.WriteFile(filepath.Join(dir, "tuta-alias"), []byte(content), 0o644)
			}
		}
		t.Error("GOBIN was not set")
		return errors.New("no GOBIN")
	}
	return args, env
}

func TestInstallCopiesBinary(t *testing.T) {
	root := sandbox(t)
	dst := filepath.Join(root, "bin", "tuta-alias") // directory does not exist yet
	args, env := fakeGoInstall(t, "NEW-BINARY", nil)

	var out bytes.Buffer
	if err := Install("main", dst, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "NEW-BINARY" {
		t.Errorf("dst = %q", got)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("dst mode = %v, want 0755", fi.Mode().Perm())
	}
	wantArgs := []string{"install", consts.ModulePath + "/cmd/tuta-alias@main"}
	if strings.Join(*args, "|") != strings.Join(wantArgs, "|") {
		t.Errorf("go args = %q, want %q", *args, wantArgs)
	}
	if !contains(*env, "GOPRIVATE="+consts.ModulePath) {
		t.Errorf("GOPRIVATE not set for the private module: %q", *env)
	}
	for _, s := range *env {
		if strings.HasPrefix(s, "GOFLAGS=") {
			t.Errorf("must not override the user's GOFLAGS: %q", s)
		}
	}
	// The streamed output reached the caller's writer.
	if !strings.Contains(out.String(), "downloading") || !strings.Contains(out.String(), "linking") {
		t.Errorf("go output was not streamed: %q", out.String())
	}
	// Nothing left behind next to dst, and the private GOBIN is gone.
	if entries, _ := os.ReadDir(filepath.Dir(dst)); len(entries) != 1 {
		t.Errorf("dst directory holds %d entries, want only the binary", len(entries))
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "tmp")); len(entries) != 0 {
		t.Errorf("build directory was not removed: %d entries", len(entries))
	}
}

func TestInstallReplacesExisting(t *testing.T) {
	root := sandbox(t)
	dst := filepath.Join(root, "bin", "tuta-alias")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeGoInstall(t, "NEW", nil)
	if err := Install("main", dst, nil); err != nil { // nil writer is allowed
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "NEW" {
		t.Errorf("dst = %q, want it replaced", got)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o755 {
		t.Errorf("dst mode = %v, want 0755", fi.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Dir(dst)); len(entries) != 1 {
		t.Errorf("dst directory holds %d entries, want only the binary", len(entries))
	}
}

func TestInstallKeepsUserGOPRIVATE(t *testing.T) {
	root := sandbox(t)
	t.Setenv("GOPRIVATE", "example.com/corp/*")
	_, env := fakeGoInstall(t, "x", nil)
	if err := Install("main", filepath.Join(root, "bin", "tuta-alias"), nil); err != nil {
		t.Fatal(err)
	}
	want := "GOPRIVATE=example.com/corp/*," + consts.ModulePath
	if !contains(*env, want) {
		t.Errorf("env = %q, want %q", *env, want)
	}
}

func TestInstallGoFailureLeavesDstAlone(t *testing.T) {
	root := sandbox(t)
	dst := filepath.Join(root, "bin", "tuta-alias")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeGoInstall(t, "NEW", errors.New("exit status 1"))

	var out bytes.Buffer
	err := Install("main", dst, &out)
	if err == nil {
		t.Fatal("want the go failure")
	}
	if !strings.Contains(err.Error(), "exit status 1") || !strings.Contains(err.Error(), "go install") {
		t.Errorf("error does not say what failed: %q", err)
	}
	if !strings.Contains(out.String(), "build failed") {
		t.Errorf("the failing output was not streamed: %q", out.String())
	}
	if got, _ := os.ReadFile(dst); string(got) != "OLD" {
		t.Errorf("dst was touched by a failed build: %q", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(dst)); len(entries) != 1 {
		t.Errorf("dst directory holds %d entries, want only the old binary", len(entries))
	}
}

func TestInstallFailsWhenNoBinaryProduced(t *testing.T) {
	root := sandbox(t)
	dst := filepath.Join(root, "bin", "tuta-alias")
	runGo = func(context.Context, []string, io.Writer, ...string) error { return nil } // "succeeds", builds nothing
	if err := Install("main", dst, nil); err == nil {
		t.Fatal("want an error when go produced no binary")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("dst was created although nothing was built")
	}
}

func TestInstallRejectsBadArguments(t *testing.T) {
	root := sandbox(t) // runGo fails the test if called
	if err := Install("--evil", filepath.Join(root, "x"), nil); err == nil {
		t.Error("an option-looking ref must be refused")
	}
	if err := Install("main", "", nil); err == nil {
		t.Error("an empty dst must be refused")
	}
}

func TestAvailable(t *testing.T) {
	root := sandbox(t)
	if err := Available(); err != nil {
		t.Fatalf("everything is there, got %v", err)
	}

	// A directory that does not exist yet is fine: Install creates it.
	installPath = func() string { return filepath.Join(root, "a", "b", "c", "tuta-alias") }
	if err := Available(); err != nil {
		t.Errorf("a creatable install directory was refused: %v", err)
	}

	// Everything missing at once: the error names all three.
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	file := filepath.Join(root, "afile")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	installPath = func() string { return filepath.Join(file, "sub", "tuta-alias") } // under a regular file, so unwritable even for root
	err := Available()
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"git", "go", "write", file} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// Only go missing: only go is named.
	lookPath = func(name string) (string, error) {
		if name == "go" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}
	installPath = func() string { return filepath.Join(root, "bin", "tuta-alias") }
	err = Available()
	if err == nil || !strings.Contains(err.Error(), "go is not on PATH") || strings.Contains(err.Error(), "git is not") {
		t.Errorf("want exactly go named, got %v", err)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// git reads .git/config of the repository it is started in, and a repository
// that arrived as an archive can say anything there: url.<base>.insteadOf to
// send the check to another server, core.sshCommand or credential.helper to
// run a command of its own. The check asks about this program's repository,
// not the one the user happens to be standing in, so git must not start there.
// `ls-remote --get-url` prints the URL after insteadOf is applied and talks to
// nobody, which makes the effect visible without a network.
func TestRunGitIgnoresTheWorkingDirectorysRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	for _, d := range []string{"home", "tmp", "downloaded-project"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A git that sees only what this test gives it: no user or system
	// configuration, no repository named by the environment, and a temp
	// directory that is not inside a repository either.
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CEILING_DIRECTORIES", "GIT_CONFIG_GLOBAL"} {
		t.Setenv(k, "") // registers the restore
		os.Unsetenv(k)
	}

	hostile := filepath.Join(root, "downloaded-project")
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "url.https://attacker.invalid/x.insteadOf", consts.RepoURL},
	} {
		cmd := exec.Command("git", append([]string{"-C", hostile}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(hostile)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := runGit(ctx, "ls-remote", "--get-url", consts.RepoURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != consts.RepoURL {
		t.Errorf("git read the working directory's repository: the check would ask %q instead of %q", got, consts.RepoURL)
	}
}

// git relays what the remote and the helpers it starts wrote (ssh, a
// credential helper, a proxy), and this text is printed on a terminal and
// written to the log. A control character in it is run by the terminal, not
// shown: OSC 52 sets the clipboard, CSI moves the cursor and clears the screen.
func TestCheckGitErrorHasNoControlCharacters(t *testing.T) {
	sandbox(t)
	fakeGit("", errors.New("fatal: remote error: \x1b]52;c;ZXZpbA==\a\x1b[2J \u009b31m \x9b \x00 done\r\n"))
	_, err := Check("main")
	if err == nil {
		t.Fatal("want the git error")
	}
	got := err.Error()
	for _, r := range got {
		if unicode.IsControl(r) {
			t.Errorf("the error holds the control character %U: %q", r, got)
		}
	}
	if !utf8.ValidString(got) {
		t.Errorf("the error is not valid UTF-8, and a stray 0x9b is CSI to an 8-bit terminal: %q", got)
	}
	if !strings.Contains(got, "remote error") || !strings.Contains(got, "done") {
		t.Errorf("the error lost the text around the control characters: %q", got)
	}
}
