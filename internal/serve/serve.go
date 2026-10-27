// Package serve ports tuta_alias/serve.py — a localhost REST API for
// scripts, status widgets and browser userscripts.
package serve

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/atomicfile"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/services"
	"github.com/tdwgm/tuta-alias/internal/session"
)

var corsHeaders = map[string]string{
	"Access-Control-Allow-Origin":  "*",
	"Access-Control-Allow-Methods": "GET, POST, DELETE, OPTIONS",
	"Access-Control-Allow-Headers": "Content-Type, Authorization",
	"Access-Control-Max-Age":       "86400",
}

var hintRE = regexp.MustCompile(`^[a-zA-Z0-9-]{1,32}$`)

// tokenDir is the cache directory shared with the session and the log, or ""
// when neither XDG_CACHE_HOME nor a home directory says where it is.
//
// Only an absolute path counts. The XDG spec says a relative XDG_CACHE_HOME is
// invalid and must be ignored, and a relative path here is not harmless: the
// token would be written into whatever directory the command ran in, and
// every other directory would get a different one. The same goes for the
// home directory, whose error used to be discarded: with no HOME,
// filepath.Join("", ".cache", ...) is a relative path.
func tokenDir() string {
	if x := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(x) {
		return filepath.Join(x, "tuta-alias")
	}
	h, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(h) {
		return ""
	}
	return filepath.Join(h, ".cache", "tuta-alias")
}

// errNoTokenDir is what the functions that need the token file say when there
// is nowhere to keep one.
var errNoTokenDir = errors.New("nowhere to keep the API token: HOME is not an absolute path and XDG_CACHE_HOME is not set to one")

// TokenPath returns the API token file path, or "" when there is nowhere to
// keep one.
func TokenPath() string {
	d := tokenDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "api-token")
}

// GetOrCreateToken reads the existing token or generates a new one.
//
// An empty or whitespace-only file counts as no token, not as the empty
// token. `> api-token` in a shell, or a crash between truncating and writing,
// used to leave a file that read back as "" and was never regenerated: serve
// then started with an empty expected token, and checkAuth compared the empty
// string with itself.
//
// A file that cannot be read is an error, not a missing token. It may hold a
// good one, and replacing it locks out every client that has it: a descriptor
// limit that a flood of connections can reach, or a permission problem, would
// otherwise rotate the token on the next request. checkAuth refuses the request
// on the error.
func GetOrCreateToken() (string, error) {
	p := TokenPath()
	if p == "" {
		return "", errNoTokenDir
	}
	raw, err := os.ReadFile(p)
	switch {
	case err == nil:
		if tok := strings.TrimSpace(string(raw)); tok != "" {
			return tok, nil
		}
	case !errors.Is(err, os.ErrNotExist):
		return "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(buf)
	// Atomically, because a half-written token file is an empty one, and an
	// empty one is what the paragraph above is about. atomicfile creates the
	// directory too.
	if err := atomicfile.WriteMkdir(p, []byte(tok+"\n"), 0o600, 0o700); err != nil {
		return "", err
	}
	return tok, nil
}

// PeekToken returns the token if one has been created, without creating one.
// The Config screen shows whether a token exists, and calling
// GetOrCreateToken for that wrote the file as a side effect of opening a
// settings screen.
func PeekToken() (string, error) {
	p := TokenPath()
	if p == "" {
		return "", nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// groupAndOther are the permission bits that let anyone but the owner in.
const groupAndOther os.FileMode = 0o077

// chmodFile is how secureToken changes a mode. A seam for tests: a file this
// process owns can always be chmod-ed (root can chmod any), so the failure
// that has to be handled cannot be had for real.
var chmodFile = (*os.File).Chmod

// looseMode is the permission bits of a regular file when they let anyone but
// its owner in, and 0 otherwise. Something that is not a regular file at the
// token's path is not a token file with the wrong mode, it is something else.
func looseMode(fi os.FileInfo) os.FileMode {
	if perm := fi.Mode().Perm(); fi.Mode().IsRegular() && perm&groupAndOther != 0 {
		return perm
	}
	return 0
}

// LooseTokenMode returns the permission bits of the token file when they let
// anyone but its owner in, and 0 when the file is private or does not exist. It
// only looks, which is what a diagnostic needs: a checklist that fixes what it
// reports is not one. Run is what makes the file private.
func LooseTokenMode() os.FileMode {
	fi, err := os.Stat(TokenPath())
	if err != nil {
		return 0
	}
	return looseMode(fi)
}

// secureToken takes group and other access off a token file that was already
// there, and says what it did on w and in the log.
//
// A token this program creates is 0600, but one that exists is used as it is:
// `echo token > api-token` under a 022 umask, a copy, a restore from a backup
// and a sync tool all leave it 0644, and the directory around it is not always
// the 0700 this program makes. The token is the only thing between another
// account on the machine and the API, so it is not served from a file they can
// read. Refusing to start instead was weighed and left out: serve runs
// unattended, and a service that stays down until someone reads its log
// protects nothing that the same chmod, done for the user, does not. When the
// chmod fails (the file is another user's, or the filesystem has no modes)
// serve still starts, and the failure is said just the same.
//
// Making the file private cannot take back a copy somebody already read, which
// is why the notice says how to replace the token. That is not done here:
// every client has to be updated afterwards, so it stays a deliberate action.
func secureToken(w io.Writer) {
	p := TokenPath()
	if p == "" {
		return
	}
	// Opened through the directory it lives in, which refuses a symlink that
	// leaves it: whoever can write there must not be able to point the chmod
	// below at some other file. The mode is changed on the open file, so it
	// is also the file that was checked.
	root, err := os.OpenRoot(filepath.Dir(p))
	if err != nil {
		return
	}
	f, err := root.Open(filepath.Base(p))
	_ = root.Close() // the open file does not need it
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	was := looseMode(fi)
	if was == 0 {
		return
	}
	// Bits are taken away and never added, so the owner keeps what was there.
	now := was &^ groupAndOther
	if err := chmodFile(f, now); err != nil {
		fmt.Fprintf(w, "Warning: %s is open to other users (mode %04o) and could not be made private: %v\n"+
			"         Serving anyway. Make it private yourself (chmod 600), and if someone else could have\n"+
			"         read the token, replace it: tuta-alias serve --rotate-token\n", p, was, err)
		logx.Errorf("%s is open to other users (mode %04o) and could not be made private: %v", p, was, err)
		return
	}
	fmt.Fprintf(w, "Warning: %s was open to other users (mode %04o) and is mode %04o now.\n"+
		"         If someone else could have read the token, replace it: tuta-alias serve --rotate-token\n", p, was, now)
	logx.Errorf("%s was open to other users (mode %04o) and is mode %04o now", p, was, now)
}

// RotateToken replaces the token with a fresh one and returns it. Every
// client has to be updated afterwards, which is why it is a deliberate action
// and not something `serve` does on its own.
func RotateToken() (string, error) {
	p := TokenPath()
	if p == "" {
		return "", errNoTokenDir
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return GetOrCreateToken()
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	for k, v := range corsHeaders {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload != nil {
		_ = json.NewEncoder(w).Encode(payload)
	}
}

func writeStatus(w http.ResponseWriter, status int) {
	for k, v := range corsHeaders {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
}

func checkAuth(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	// Not trimmed: the stored token is written by this program and carries no
	// whitespace, while strings.TrimSpace removes Unicode spaces that the
	// HTTP header parser keeps. `Authorization: Bearer \u00a0` used to trim
	// down to "" and match an empty expected token.
	provided := strings.TrimPrefix(auth, "Bearer ")
	expected, err := GetOrCreateToken()
	if err != nil || expected == "" {
		// Fail closed: an empty expected token must never authorise anything,
		// whatever produced it.
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func newRouter() http.Handler {
	mux := http.NewServeMux()

	// healthz stays reachable without a token so a status widget can ask
	// "is there a session?" — but it no longer hands out WHOSE session.
	// Access-Control-Allow-Origin is "*", so any page the user visited could
	// read the account's address from it cross-origin. The address now needs
	// the same bearer token as every other route.
	mux.HandleFunc("/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "OPTIONS" {
			writeStatus(w, 204)
			return
		}
		s, err := session.Load()
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "mailAddress": nil})
			return
		}
		body := map[string]any{"ok": true, "mailAddress": nil}
		if checkAuth(r) {
			body["mailAddress"] = s.MailAddress
		}
		writeJSON(w, 200, body)
	})

	// Generic handler factory for routes that need auth + session + client.
	withSession := func(fn func(w http.ResponseWriter, r *http.Request, c *api.Client, s *session.Session)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "OPTIONS" {
				writeStatus(w, 204)
				return
			}
			if !checkAuth(r) {
				writeJSON(w, 401, map[string]string{"error": "unauthorized — include Authorization: Bearer <token>"})
				return
			}
			s, err := session.Load()
			if err != nil {
				writeJSON(w, 401, map[string]string{"error": "no session — run `tuta-alias login` first"})
				return
			}
			c := api.NewClient(consts.BaseURL, s.AccessToken)
			fn(w, r, c, s)
		}
	}

	mux.HandleFunc("/v1/whoami", withSession(func(w http.ResponseWriter, r *http.Request, c *api.Client, s *session.Session) {
		if r.Method != "GET" {
			writeJSON(w, 404, map[string]string{"error": "no route"})
			return
		}
		writeJSON(w, 200, map[string]string{"mailAddress": s.MailAddress})
	}))

	mux.HandleFunc("/v1/domains", withSession(func(w http.ResponseWriter, r *http.Request, c *api.Client, s *session.Session) {
		if r.Method != "GET" {
			writeJSON(w, 404, map[string]string{"error": "no route"})
			return
		}
		doms, err := services.ListDomains(c, s)
		if err != nil {
			writeJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, doms)
	}))

	mux.HandleFunc("/v1/aliases", withSession(func(w http.ResponseWriter, r *http.Request, c *api.Client, s *session.Session) {
		if r.Method != "GET" {
			writeJSON(w, 404, map[string]string{"error": "no route"})
			return
		}
		grouped, err := services.AliasesByDomain(c, s)
		if err != nil {
			writeJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		if d := r.URL.Query().Get("domain"); d != "" {
			writeJSON(w, 200, grouped[d])
			return
		}
		writeJSON(w, 200, grouped)
	}))

	mux.HandleFunc("/v1/quota", withSession(func(w http.ResponseWriter, r *http.Request, c *api.Client, s *session.Session) {
		if r.Method != "GET" {
			writeJSON(w, 404, map[string]string{"error": "no route"})
			return
		}
		q, err := services.AliasQuota(c, s)
		if err != nil {
			writeJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, q)
	}))

	mux.HandleFunc("/v1/alias/suggest", withSession(func(w http.ResponseWriter, r *http.Request, c *api.Client, s *session.Session) {
		if r.Method != "GET" {
			writeJSON(w, 404, map[string]string{"error": "no route"})
			return
		}
		domain := r.URL.Query().Get("domain")
		if domain == "" {
			writeJSON(w, 400, map[string]string{"error": "missing required query parameter: domain"})
			return
		}
		valid, err := services.ListDomains(c, s)
		if err != nil {
			writeJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		ok := false
		for _, d := range valid {
			if d == domain {
				ok = true
				break
			}
		}
		if !ok {
			writeJSON(w, 400, map[string]string{"error": fmt.Sprintf("unknown domain: %q", domain)})
			return
		}
		hint := r.URL.Query().Get("hint")
		if hint == "" || !hintRE.MatchString(hint) {
			hint = "tuta"
		}
		suggestion, err := services.SuggestAlias(hint, domain)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]string{"mailAddress": suggestion})
	}))

	// /v1/alias (POST create) and /v1/alias/<email> (DELETE).
	mux.HandleFunc("/v1/alias", withSession(func(w http.ResponseWriter, r *http.Request, c *api.Client, s *session.Session) {
		if r.Method != "POST" {
			writeJSON(w, 404, map[string]string{"error": "no route"})
			return
		}
		// Bounded: io.ReadAll on a request body is an unbounded allocation,
		// and "it only listens on localhost" is not a memory limit.
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid JSON body"})
			return
		}
		mailAddress, _ := payload["mailAddress"].(string)
		mailAddress = strings.TrimSpace(mailAddress)
		if mailAddress == "" || !strings.Contains(mailAddress, "@") {
			writeJSON(w, 400, map[string]string{"error": "mailAddress must be a non-empty string with '@'"})
			return
		}
		if err := services.AddAlias(c, s, mailAddress); err != nil {
			writeJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"created": true, "mailAddress": mailAddress})
	}))

	mux.HandleFunc("/v1/alias/", withSession(func(w http.ResponseWriter, r *http.Request, c *api.Client, s *session.Session) {
		if r.Method != "DELETE" {
			writeJSON(w, 404, map[string]string{"error": "no route"})
			return
		}
		email := strings.TrimPrefix(r.URL.Path, "/v1/alias/")
		if email == "" {
			writeJSON(w, 400, map[string]string{"error": "missing email in path"})
			return
		}
		restore := false
		if v := r.URL.Query().Get("restore"); v == "true" || v == "1" || v == "yes" {
			restore = true
		}
		if err := services.RemoveAlias(c, s, email, restore); err != nil {
			writeJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"removed": true, "mailAddress": email})
	}))

	return mux
}

// maxRequestBytes bounds a request body (the only one is a small JSON object).
const maxRequestBytes = 64 << 10

// IsLoopback reports whether host keeps the API on this machine.
func IsLoopback(host string) bool {
	h := strings.Trim(strings.TrimSpace(host), "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// hostGuard rejects requests whose Host header is not an address this server
// is reachable at.
//
// CORS does not protect a localhost API from the web: a page that resolves
// its own domain to 127.0.0.1 (DNS rebinding) is same-origin as far as the
// browser is concerned, and the browser sends that domain as the Host header.
// Checking Host is what closes it, and it costs a legitimate client nothing.
// Binding to a non-loopback address is taken as "I want this reachable from
// elsewhere", so the guard then steps aside.
func hostGuard(bind string, next http.Handler) http.Handler {
	if bind != "" && !IsLoopback(bind) {
		return next
	}
	allowed := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	if bind != "" {
		allowed[strings.Trim(bind, "[]")] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !allowed[strings.Trim(host, "[]")] {
			writeJSON(w, 403, map[string]string{
				"error": "forbidden: this API only answers requests addressed to localhost",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler is the full HTTP handler for a server bound to bind.
func Handler(bind string) http.Handler { return hostGuard(bind, newRouter()) }

// Run binds an http.Server on host:port and starts serving until Ctrl-C. A
// token file that other users can read is made private first, and a message on
// stderr and in the log says so.
func Run(host string, port int) error {
	if _, err := GetOrCreateToken(); err != nil {
		return err
	}
	secureToken(os.Stderr)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	srv := &http.Server{
		Addr:    addr,
		Handler: Handler(host),
		// A server without timeouts keeps a connection that never finishes
		// its request open forever.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}
