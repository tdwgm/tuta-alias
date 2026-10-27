package serve

import (
	"bytes"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// writeToken puts a token file at TokenPath with exactly this mode. The umask
// would narrow what WriteFile asks for, and a loose mode is the point.
func writeToken(t *testing.T, token string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(tokenDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// A file left read-only by an earlier call cannot be opened for writing.
	if err := os.Remove(TokenPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.WriteFile(TokenPath(), []byte(token+"\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(TokenPath(), mode); err != nil {
		t.Fatal(err)
	}
}

// modeOf is the permission bits of the file at path.
func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestHealthz_NoAuth(t *testing.T) {
	h := newRouter()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/healthz", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"ok"`) {
		t.Errorf("body missing 'ok': %s", rr.Body.String())
	}
}

func TestUnauthorized_WithoutBearer(t *testing.T) {
	h := newRouter()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/whoami", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCORS_OptionsReturns204(t *testing.T) {
	h := newRouter()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/v1/aliases", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != 204 {
		t.Errorf("status = %d, want 204", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Errorf("missing CORS header")
	}
}

func TestUnknownRoute_404(t *testing.T) {
	h := newRouter()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/bogus", nil)
	req.Header.Set("Authorization", "Bearer foo")
	h.ServeHTTP(rr, req)
	if rr.Code != 401 && rr.Code != 404 {
		t.Errorf("expected 401 (bad auth) or 404, got %d", rr.Code)
	}
}

// healthz answers without a token so a status widget can ask "is there a
// session?", but it must not hand out WHOSE session: the CORS header is "*",
// so any page the user visits could read the account's address from it.
func TestHealthz_HidesAddressWithoutToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	sess := &session.Session{MailAddress: "u@example.com", AccessToken: "t"}
	if _, err := session.Save(sess); err != nil {
		t.Fatal(err)
	}
	tok, err := GetOrCreateToken()
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, auth string
		wantAddr   bool
	}{
		{"no token", "", false},
		{"wrong token", "Bearer nope", false},
		{"right token", "Bearer " + tok, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/v1/healthz", nil)
			if c.auth != "" {
				req.Header.Set("Authorization", c.auth)
			}
			newRouter().ServeHTTP(rr, req)
			if rr.Code != 200 {
				t.Fatalf("status = %d", rr.Code)
			}
			if got := strings.Contains(rr.Body.String(), "u@example.com"); got != c.wantAddr {
				t.Errorf("body discloses the address = %v, want %v: %s", got, c.wantAddr, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), `"ok":true`) {
				t.Errorf("healthz should still report ok: %s", rr.Body.String())
			}
		})
	}
}

// DNS rebinding: a page that points its own domain at 127.0.0.1 is
// same-origin as far as the browser is concerned, so CORS does not stop it.
// Checking the Host header does.
func TestHostGuard(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	h := Handler("127.0.0.1")
	for _, c := range []struct {
		host string
		want int
	}{
		{"127.0.0.1:8088", 200},
		{"localhost:8088", 200},
		{"[::1]:8088", 200},
		{"evil.example.com:8088", 403},
		{"attacker.test", 403},
	} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/healthz", nil)
		req.Host = c.host
		h.ServeHTTP(rr, req)
		if rr.Code != c.want {
			t.Errorf("Host %q: status = %d, want %d", c.host, rr.Code, c.want)
		}
	}

	// Bound to a non-loopback address on purpose: the guard steps aside,
	// because then being reachable by another name is the point.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/healthz", nil)
	req.Host = "nas.example.com:8088"
	Handler("0.0.0.0").ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("non-loopback bind: status = %d, want 200", rr.Code)
	}
}

func TestIsLoopback(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1": true, "localhost": true, "::1": true, "[::1]": true,
		"127.1.2.3": true, "0.0.0.0": false, "192.168.1.5": false, "": false,
	} {
		if got := IsLoopback(host); got != want {
			t.Errorf("IsLoopback(%q) = %v, want %v", host, got, want)
		}
	}
}

// PeekToken must not create the token: the Config screen shows whether one
// exists, and creating it as a side effect of opening a settings screen
// writes a secret nobody asked for.
func TestPeekTokenDoesNotCreate(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	tok, err := PeekToken()
	if err != nil || tok != "" {
		t.Fatalf("PeekToken on a fresh cache = %q, %v", tok, err)
	}
	if _, err := os.Stat(TokenPath()); !os.IsNotExist(err) {
		t.Error("PeekToken created the token file")
	}
	first, err := GetOrCreateToken()
	if err != nil || first == "" {
		t.Fatal(err)
	}
	second, err := RotateToken()
	if err != nil || second == "" {
		t.Fatal(err)
	}
	if first == second {
		t.Error("RotateToken returned the same token")
	}
	if got, _ := PeekToken(); got != second {
		t.Errorf("PeekToken = %q after rotation, want %q", got, second)
	}
}

// doctor reports from this, so it must only look: a checklist that fixes what
// it reports is not one.
func TestLooseTokenMode(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if got := LooseTokenMode(); got != 0 {
		t.Errorf("no token file: LooseTokenMode = %04o, want 0", got)
	}
	for _, c := range []struct {
		name       string
		mode, want os.FileMode
	}{
		{"private", 0o600, 0},
		{"stricter than private", 0o400, 0},
		{"the group can read it", 0o640, 0o640},
		{"everyone can read it", 0o644, 0o644},
		{"only others can read it", 0o604, 0o604},
	} {
		t.Run(c.name, func(t *testing.T) {
			writeToken(t, "tok", c.mode)
			if got := LooseTokenMode(); got != c.want {
				t.Errorf("LooseTokenMode = %04o, want %04o", got, c.want)
			}
			if got := modeOf(t, TokenPath()); got != c.mode {
				t.Errorf("looking changed the mode to %04o", got)
			}
		})
	}
	t.Run("a directory is not a token file", func(t *testing.T) {
		if err := os.Remove(TokenPath()); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(TokenPath(), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := LooseTokenMode(); got != 0 {
			t.Errorf("LooseTokenMode = %04o for a directory, want 0", got)
		}
	})
}

// A token written by this program is 0600, but one that was already there is
// used as it is, and `echo token > api-token` under a 022 umask, a copy and a
// restore from a backup all leave it 0644. Whoever else has an account on the
// machine can then read the only secret between them and the API, so serve
// makes the file private before it listens. It does not refuse to start
// instead: serve runs unattended, and a service that stays down until
// someone reads its log protects nothing that the same chmod, done for the
// user, does not.
func TestSecureTokenMakesALooseFilePrivate(t *testing.T) {
	const typed = "the-token-somebody-typed"
	for _, c := range []struct {
		name       string
		mode, want os.FileMode
	}{
		{"everyone can read it", 0o644, 0o600},
		{"the group can read it", 0o640, 0o600},
		{"the group can write it", 0o660, 0o600},
		{"only others can read it", 0o604, 0o600},
		{"everyone can do everything", 0o666, 0o600},
		// Bits are taken away and never added: the owner keeps what was there.
		{"executable by everyone", 0o755, 0o700},
		{"private already", 0o600, 0o600},
		{"stricter than private", 0o400, 0o400},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			writeToken(t, typed, c.mode)

			var out bytes.Buffer
			secureToken(&out)

			if got := modeOf(t, TokenPath()); got != c.want {
				t.Errorf("mode = %04o, want %04o", got, c.want)
			}
			// The token is what the clients hold, so making its file private
			// must not replace it: rotating is a deliberate action.
			if got, err := GetOrCreateToken(); err != nil || got != typed {
				t.Errorf("GetOrCreateToken = %q, %v; want the token that was there", got, err)
			}
			if c.mode == c.want {
				if out.Len() != 0 {
					t.Errorf("said %q about a file that was fine", out.String())
				}
				return
			}
			for _, want := range []string{TokenPath(), fmt.Sprintf("%04o", c.mode), "--rotate-token"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("the notice lacks %q: %q", want, out.String())
				}
			}
			if strings.Contains(out.String(), typed) {
				t.Errorf("the notice carries the token: %q", out.String())
			}
		})
	}
}

// A file that cannot be made private is no reason to stop, so it is said and
// served as it is. A file this process owns can always be chmod-ed (root can
// chmod any), so the failure comes from the seam.
func TestSecureTokenKeepsServingWhenItCannotChmod(t *testing.T) {
	const typed = "the-token-somebody-typed"
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	writeToken(t, typed, 0o640)
	prev := chmodFile
	t.Cleanup(func() { chmodFile = prev })
	chmodFile = func(*os.File, os.FileMode) error { return errors.New("operation not permitted") }

	var out bytes.Buffer
	secureToken(&out)

	if got := modeOf(t, TokenPath()); got != 0o640 {
		t.Errorf("mode = %04o, want it left at 0640", got)
	}
	for _, want := range []string{"0640", "could not be made private", "operation not permitted"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the notice lacks %q: %q", want, out.String())
		}
	}
	if got, err := GetOrCreateToken(); err != nil || got != typed {
		t.Errorf("GetOrCreateToken = %q, %v; want the token, still served", got, err)
	}
}

// A symlink at the token path is somebody's arrangement, and the file it points
// to is not this program's to chmod. Followed, it would be a way to take group
// and other access off any file of whoever runs serve, for anyone who can
// write that directory: a program run as root with another user's HOME is the
// case. The token is opened through the directory it lives in, which refuses a
// link that leaves it.
func TestSecureTokenDoesNotFollowASymlinkOutOfItsDirectory(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	if err := os.MkdirAll(tokenDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(cache, "victim")
	if err := os.WriteFile(victim, []byte("not a token file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(victim, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "victim"), TokenPath()); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	secureToken(&bytes.Buffer{})

	if got := modeOf(t, victim); got != 0o644 {
		t.Errorf("the file the link points to has mode %04o, want 0644: it was chmod-ed through the link", got)
	}
}

// serve may run where nobody sees its stderr, so what it did to the token file
// is in the log as well, at the level the default LOG_LEVEL keeps. The token
// itself is never written there.
func TestSecureTokenIsLogged(t *testing.T) {
	const typed = "the-token-somebody-typed"
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	logFile := filepath.Join(t.TempDir(), "tuta-alias.log")
	cfg := config.Default()
	for k, v := range map[string]string{"LOG_LEVEL": "error", "LOG_FILE": logFile} {
		if err := config.Apply(cfg, k, v); err != nil {
			t.Fatal(err)
		}
	}
	logx.Setup(cfg)
	t.Cleanup(func() { logx.Setup(nil) })
	writeToken(t, typed, 0o644)

	secureToken(&bytes.Buffer{})

	raw, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("nothing was logged: %v", err)
	}
	for _, want := range []string{"api-token", "0644"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the log lacks %q: %s", want, raw)
		}
	}
	if strings.Contains(string(raw), typed) {
		t.Errorf("the log carries the token: %s", raw)
	}
}

// A request body is bounded: localhost is not a memory limit.
func TestCreateAliasBodyIsBounded(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	tok, err := GetOrCreateToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Save(&session.Session{MailAddress: "u@example.com", AccessToken: "t"}); err != nil {
		t.Fatal(err)
	}
	// Valid JSON, so the only thing that can make it unparseable is the
	// bound truncating it. Asserting "not 200" proved nothing: this request
	// has no reachable Tuta behind it, so it cannot answer 200 either way.
	huge := strings.NewReader(`{"mailAddress":"` + strings.Repeat("a", maxRequestBytes+1024) + `@example.com"}`)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/alias", huge)
	req.Header.Set("Authorization", "Bearer "+tok)
	newRouter().ServeHTTP(rr, req)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "invalid JSON body") {
		t.Errorf("status = %d, body = %s; want 400 invalid JSON body, which only "+
			"a truncated body produces", rr.Code, strings.TrimSpace(rr.Body.String()))
	}
}

// An empty token file counts as no token. `> api-token` in a shell, or a
// crash between truncating and writing, used to leave a file that read back
// as "" and was never regenerated: serve then started with an empty expected
// token, and the bearer check compared the empty string with itself.
func TestEmptyTokenFileIsRegenerated(t *testing.T) {
	for _, content := range []string{"", "\n", "   \n\t\n"} {
		t.Run(strings.ReplaceAll(content, "\n", "\\n"), func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			if err := os.MkdirAll(tokenDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(TokenPath(), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			tok, err := GetOrCreateToken()
			if err != nil {
				t.Fatal(err)
			}
			if tok == "" {
				t.Fatal("GetOrCreateToken returned the empty token")
			}
			onDisk, err := os.ReadFile(TokenPath())
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(onDisk)) != tok {
				t.Errorf("file holds %q, GetOrCreateToken said %q", strings.TrimSpace(string(onDisk)), tok)
			}
			if info, err := os.Stat(TokenPath()); err != nil {
				t.Fatal(err)
			} else if info.Mode().Perm() != 0o600 {
				t.Errorf("mode = %v, want 0600", info.Mode().Perm())
			}
			// And the regenerated token is what authorises.
			if _, err := session.Save(&session.Session{MailAddress: "u@example.com", AccessToken: "t"}); err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/v1/whoami", nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			newRouter().ServeHTTP(rr, req)
			if rr.Code != 200 {
				t.Errorf("status with the regenerated token = %d, want 200", rr.Code)
			}
		})
	}
}

// strings.TrimSpace removes Unicode spaces that the HTTP header parser keeps,
// so `Authorization: Bearer \u00a0` used to trim down to "" and match an
// empty expected token. Nothing may authorise but the token itself.
func TestBearerIsNotTrimmed(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	tok, err := GetOrCreateToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Save(&session.Session{MailAddress: "u@example.com", AccessToken: "t"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"the token", "Bearer " + tok, 200},
		{"no-break space", "Bearer \u00a0", 401},
		{"ideographic space", "Bearer \u3000", 401},
		{"nothing after Bearer", "Bearer ", 401},
		{"a tab", "Bearer \t", 401},
		{"the token with trailing space", "Bearer " + tok + " ", 401},
		{"a wrong token", "Bearer " + strings.Repeat("a", len(tok)), 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/v1/whoami", nil)
			req.Header.Set("Authorization", c.header)
			newRouter().ServeHTTP(rr, req)
			if rr.Code != c.want {
				t.Errorf("Authorization %q -> %d, want %d (body %s)",
					c.header, rr.Code, c.want, strings.TrimSpace(rr.Body.String()))
			}
		})
	}
}

// A token file that cannot be read is not a missing one. Treating it as
// missing replaced a good token with a new random one on the next request,
// which locks out every configured client, over an error that says nothing
// about what the file holds: a descriptor limit that a flood of connections
// can reach, or a permission problem. Only "does not exist" and "empty" mean
// there is no token. A symlink loop is the portable way to make a read fail
// with something other than "does not exist", and it fails for root too.
func TestUnreadableTokenFileIsNotReplaced(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := os.MkdirAll(tokenDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(TokenPath(), TokenPath()); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
	if _, err := os.ReadFile(TokenPath()); err == nil || os.IsNotExist(err) {
		t.Skipf("this system reports a symlink loop as %v, not as a read error", err)
	}

	tok, err := GetOrCreateToken()
	if err == nil {
		t.Fatalf("GetOrCreateToken replaced a token file it could not read, with %q", tok)
	}
	if fi, lerr := os.Lstat(TokenPath()); lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the token file that could not be read was overwritten: %v, %v", fi, lerr)
	}

	// And the server refuses instead of guessing: no token can be compared
	// with, so nothing authorises.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 43))
	newRouter().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// The XDG spec says a relative XDG_CACHE_HOME is invalid and must be ignored.
// Honoured, it puts the token in whatever directory the command ran in, and a
// different token in every other one. "~/.cache", set where nothing expands
// the tilde (systemd's environment.d, /etc/environment), is the usual way to
// get one, and it creates a directory literally named "~".
func TestTokenPathIgnoresARelativeCacheHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "~/.cache")
	want := filepath.Join(home, ".cache", "tuta-alias", "api-token")
	if got := TokenPath(); got != want {
		t.Errorf("TokenPath = %q, want %q", got, want)
	}
}

// With no home directory the old code built ".cache/tuta-alias/api-token" from
// an empty string, a path relative to the working directory, and wrote the
// secret there: into a project that is about to be committed, or a container's
// root. Nowhere to keep a token is an error, never a guess.
func TestNoCacheDirectoryMeansNoTokenFileInTheWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	if p := TokenPath(); p != "" {
		t.Errorf("TokenPath = %q, want \"\" when there is no cache directory", p)
	}
	if tok, err := GetOrCreateToken(); err == nil {
		t.Errorf("GetOrCreateToken made a token (%d characters) with nowhere to keep it", len(tok))
	}
	if tok, err := RotateToken(); err == nil {
		t.Errorf("RotateToken made a token (%d characters) with nowhere to keep it", len(tok))
	}
	if tok, err := PeekToken(); err != nil || tok != "" {
		t.Errorf("PeekToken = %q, %v; want no token and no error", tok, err)
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("the working directory now holds %d entries, the token went there", len(entries))
	}
}

// Run is the unattended entry point, so the file has to be made private from
// there and not only by a function nothing calls. A port that cannot be bound
// makes it return right after the token steps, which is what lets a test run
// it at all.
func TestRunMakesALooseTokenFilePrivate(t *testing.T) {
	const typed = "the-token-somebody-typed"
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	writeToken(t, typed, 0o644)

	if err := Run("127.0.0.1", 70000); err == nil {
		t.Fatal("Run on a port that does not exist returned nil")
	}

	if got := modeOf(t, TokenPath()); got != 0o600 {
		t.Errorf("mode after Run = %04o, want 0600", got)
	}
	if got, err := PeekToken(); err != nil || got != typed {
		t.Errorf("PeekToken = %q, %v; want the token that was there, not a new one", got, err)
	}
}
