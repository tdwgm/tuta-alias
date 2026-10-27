package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/tdwgm/tuta-alias/internal/consts"
)

// StatusInvalidSoftwareVersion is Tuta's InvalidSoftwareVersionError
// (tuta-sdk/rust/sdk/src/rest_error.rs). The server returns it for every
// request whose `cv` header is older than the minimum it accepts, which
// happens a few weeks after each Tuta release.
const StatusInvalidSoftwareVersion = 474

// versionNumberRE extracts the deployed web client's version from
// app.tuta.com/index.js, e.g. `"versionNumber": "359.260904.0"`.
var versionNumberRE = regexp.MustCompile(`"versionNumber"\s*:\s*"(\d+\.\d+\.\d+)"`)

// cachedVersionRE validates the cache file content as a whole.
var cachedVersionRE = regexp.MustCompile(`^\d{1,6}\.\d{1,8}\.\d{1,6}$`)

var (
	cvMu     sync.Mutex
	cvLoaded bool
	cvValue  string

	// refreshMu serialises refreshes so parallel 474s share one index.js
	// fetch (x/sync/singleflight is not a dependency).
	refreshMu sync.Mutex
)

// ClientVersionPath is the per-machine cache of the last resolved `cv`.
// Same directory as session.json (internal/session). Exported so
// `uninstall --purge` can remove it: it is per-machine state like the session
// and the api-token, and leaving it behind meant a purge was not a purge.
func ClientVersionPath() string { return clientVersionPath() }

// clientVersionPath is "" when there is nowhere to cache the resolved client
// version: with no HOME and no XDG_CACHE_HOME this was a path relative to the
// current directory, so the cache was written into whatever directory the
// command ran in. Reading and writing it is best effort anyway, so "" simply
// means the fallback version is used every time.
func clientVersionPath() string {
	if x := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(x) {
		return filepath.Join(x, "tuta-alias", "client-version")
	}
	h, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(h) {
		return ""
	}
	return filepath.Join(h, ".cache", "tuta-alias", "client-version")
}

// currentClientVersion returns the `cv` header value: the cached resolved
// version when it is usable, otherwise the compiled-in fallback.
func currentClientVersion() string {
	cvMu.Lock()
	defer cvMu.Unlock()
	if !cvLoaded {
		cached := ""
		if b, err := os.ReadFile(clientVersionPath()); err == nil {
			cached = strings.TrimSpace(string(b))
			if !cachedVersionRE.MatchString(cached) {
				cached = ""
			}
		}
		cvValue = pickClientVersion(cached, consts.ClientVersion)
		cvLoaded = true
	}
	return cvValue
}

// pickClientVersion decides which `cv` to send at startup, given the version
// cached from a previous refresh (may be "") and the compiled-in fallback.
// The newer one wins, so a cache written before a rebuild with a bumped
// fallback does not cost a 474 round-trip.
func pickClientVersion(cached, builtin string) string {
	if cached != "" && compareVersions(cached, builtin) > 0 {
		return cached
	}
	return builtin
}

// compareVersions compares dotted numeric versions ("359.260904.0").
// Returns -1, 0 or 1. Non-numeric parts compare as 0.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// refreshClientVersion returns a replacement for the rejected version sent.
// If a parallel refresh already moved the active `cv` past sent, that value
// is returned without a fetch. Otherwise it fetches the version the live web
// client ships (baseURL/index.js); only a version newer than sent is cached
// to disk and made the active `cv`. The caller retries only when the result
// is newer than sent.
func (c *Client) refreshClientVersion(sent string) (string, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	if cur := currentClientVersion(); compareVersions(cur, sent) > 0 {
		return cur, nil
	}
	resp, err := c.HTTP.Get(c.BaseURL + "/index.js")
	if err != nil {
		return "", fmt.Errorf("fetch client version: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch client version: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("fetch client version: %w", err)
	}
	m := versionNumberRE.FindSubmatch(body)
	if m == nil {
		return "", errors.New("fetch client version: versionNumber not found in index.js")
	}
	v := string(m[1])
	if compareVersions(v, sent) <= 0 {
		return v, nil
	}

	cvMu.Lock()
	cvValue, cvLoaded = v, true
	cvMu.Unlock()

	_ = writeCacheFile(clientVersionPath(), []byte(v+"\n"))
	return v, nil
}

// writeCacheFile writes atomically (O_EXCL temp file + rename) so it never
// follows a symlink planted at the destination.
func writeCacheFile(p string, data []byte) error {
	if p == "" {
		// Nowhere to cache it. filepath.Dir("") is ".", so without this the
		// cache landed in the current directory.
		return nil
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".client-version-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
