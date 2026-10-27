package creds

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	keyringlib "github.com/zalando/go-keyring"

	"github.com/tdwgm/tuta-alias/internal/atomicfile"
)

func TestWriteFileAtomic(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, p string)
	}{
		{name: "creates missing dir", prepare: func(*testing.T, string) {}},
		{name: "overwrites existing file", prepare: func(t *testing.T, p string) {
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("old-secret-longer"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "replaces planted symlink instead of following it", prepare: func(t *testing.T, p string) {
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(filepath.Dir(p), "target"), p); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "tuta-alias")
			p := filepath.Join(dir, "bw-session")
			c.prepare(t, p)
			// writeRebootCache goes through internal/atomicfile now; the
			// guarantees this test pins (0600, a regular file replacing a
			// planted symlink) belong to that package.
			if err := atomicfile.WriteMkdir(p, []byte("secret"), 0o600, 0o700); err != nil {
				t.Fatalf("atomicfile.WriteMkdir: %v", err)
			}
			fi, err := os.Lstat(p)
			if err != nil {
				t.Fatal(err)
			}
			if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
				t.Errorf("mode = %v, want regular 0600", fi.Mode())
			}
			if b, _ := os.ReadFile(p); string(b) != "secret" {
				t.Errorf("content = %q", b)
			}
			di, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if perm := di.Mode().Perm(); perm != 0o700 {
				t.Errorf("dir mode = %v, want 0700", perm)
			}
			if _, err := os.Stat(filepath.Join(dir, "target")); !os.IsNotExist(err) {
				t.Errorf("symlink target must not be written: %v", err)
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if e.Name() != "bw-session" {
					t.Errorf("leftover file %q", e.Name())
				}
			}
		})
	}
}

// ExpiresAt is zero for an entry that never expires, and the JSON should say
// so by not having the field. omitempty cannot: encoding/json never treats a
// struct as empty, so a "forever" entry carried "0001-01-01T00:00:00Z", a date
// nobody meant. Anything that reads the entry without knowing that zero means
// never (a script looking at the keyring, a stricter parser) would take it for
// an entry that expired in the year 1.
func TestForeverEntryCarriesNoExpiry(t *testing.T) {
	keyringlib.MockInit()
	if err := Cache("forever-user", "kept", "forever"); err != nil {
		t.Fatal(err)
	}
	raw, err := keyringlib.Get(keyringService, "forever-user")
	if err != nil {
		t.Fatal(err)
	}

	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("the entry is not JSON: %v: %s", err, raw)
	}
	if _, has := fields["expiresAt"]; has {
		t.Errorf("a forever entry carries an expiry: %s", raw)
	}
	if fields["secret"] != "kept" {
		t.Errorf("the secret is not in the entry: %s", raw)
	}

	// A build from before the change reads the entry as it reads any without an
	// expiry: the field is absent, so the time stays zero. The tag option only
	// decides what is written, so this is that build's reader.
	var before struct {
		Secret    string    `json:"secret"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal([]byte(raw), &before); err != nil || before.Secret != "kept" || !before.ExpiresAt.IsZero() {
		t.Errorf("an older build reads %+v, %v from %s; want the secret and no expiry", before, err, raw)
	}

	if got, ok := Get("forever-user", "forever"); !ok || got != "kept" {
		t.Errorf("Get = %q, %v; want the secret", got, ok)
	}
	if st := Status("forever-user", "forever"); !st.Exists || st.Mode != "forever" || !st.ExpiresAt.IsZero() {
		t.Errorf("Status = %+v; want an entry that exists and never expires", st)
	}
}

// An entry that does expire still says when, in a form that reads back, so
// dropping the field from the other kind did not drop it from this one.
func TestDurationEntryKeepsItsExpiry(t *testing.T) {
	keyringlib.MockInit()
	start := time.Now()
	if err := Cache("timed-user", "kept", "8h"); err != nil {
		t.Fatal(err)
	}
	raw, err := keyringlib.Get(keyringService, "timed-user")
	if err != nil {
		t.Fatal(err)
	}
	var e struct {
		Secret    string    `json:"secret"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatalf("the entry is not JSON: %v: %s", err, raw)
	}
	if e.ExpiresAt.Before(start.Add(8*time.Hour)) || e.ExpiresAt.After(time.Now().Add(8*time.Hour)) {
		t.Errorf("expiresAt = %v, want 8h after %v: %s", e.ExpiresAt, start, raw)
	}
	if got, ok := Get("timed-user", "8h"); !ok || got != "kept" {
		t.Errorf("Get = %q, %v; want the secret", got, ok)
	}
	if st := Status("timed-user", "8h"); !st.Exists || st.Mode != "duration" || !st.ExpiresAt.Equal(e.ExpiresAt) {
		t.Errorf("Status = %+v; want a duration entry that expires at %v", st, e.ExpiresAt)
	}
}

// Every "forever" entry already in a keyring was written with the zero time
// spelled out. They have to go on reading as never expiring, or the upgrade
// would drop each saved login and cached master password it finds.
func TestEntryWrittenWithAZeroExpiryStillNeverExpires(t *testing.T) {
	keyringlib.MockInit()
	const written = `{"secret":"kept","expiresAt":"0001-01-01T00:00:00Z"}`
	if err := keyringlib.Set(keyringService, "old-user", written); err != nil {
		t.Fatal(err)
	}
	for _, ttl := range []string{"forever", "8h"} {
		if got, ok := Get("old-user", ttl); !ok || got != "kept" {
			t.Errorf("Get(%q) = %q, %v; want the secret", ttl, got, ok)
		}
		if st := Status("old-user", ttl); !st.Exists || !st.ExpiresAt.IsZero() {
			t.Errorf("Status(%q) = %+v; want an entry that exists and never expires", ttl, st)
		}
	}
}

func TestPurgeCaches(t *testing.T) {
	keyringlib.MockInit()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	db := filepath.Join(t.TempDir(), "tuta.kdbx")
	for _, k := range []string{bwSessionKey, savedLoginKey, KeePassCacheKey(db), "unrelated"} {
		if err := Cache(k, "s", "forever"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeRebootCache(bwSessionKey, "s"); err != nil {
		t.Fatal(err)
	}

	if err := PurgeCaches(db); err != nil {
		t.Fatalf("PurgeCaches: %v", err)
	}
	for _, k := range []string{bwSessionKey, savedLoginKey, KeePassCacheKey(db)} {
		if _, ok := Get(k, "forever"); ok {
			t.Errorf("keyring entry %q survived purge", k)
		}
	}
	if _, ok := readRebootCache(bwSessionKey); ok {
		t.Error("reboot-cached bw session survived purge")
	}
	if _, ok := Get("unrelated", "forever"); !ok {
		t.Error("purge must only touch tuta-alias's own keys")
	}
}
