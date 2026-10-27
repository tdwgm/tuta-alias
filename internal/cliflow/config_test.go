package cliflow

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	keyringlib "github.com/zalando/go-keyring"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/creds"
	"github.com/tdwgm/tuta-alias/internal/serve"
)

// uninstall --purge must also remove the saved login, cached secrets and the
// API token, not just config + session.
func TestUninstall_PurgeClearsKeyringAndToken(t *testing.T) {
	keyringlib.MockInit()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	db := filepath.Join(t.TempDir(), "tuta.kdbx")
	cfg := config.Default()
	cfg.KeePassDB = db
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := creds.SaveLogin("u@tuta.com", "pw"); err != nil {
		t.Fatal(err)
	}
	if err := creds.Cache(creds.KeePassCacheKey(db), "master", "forever"); err != nil {
		t.Fatal(err)
	}
	if _, err := serve.GetOrCreateToken(); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := Uninstall(&out, &errOut, true); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if _, err := creds.LoadLogin(); !errors.Is(err, creds.ErrNoSavedLogin) {
		t.Errorf("saved login survived purge: %v", err)
	}
	if _, ok := creds.Get(creds.KeePassCacheKey(db), "forever"); ok {
		t.Error("KeePass master survived purge")
	}
	if _, err := os.Stat(serve.TokenPath()); !os.IsNotExist(err) {
		t.Errorf("api token survived purge: %v", err)
	}
	if _, err := os.Stat(config.Path()); !os.IsNotExist(err) {
		t.Errorf("config survived purge: %v", err)
	}
	if !strings.Contains(out.String(), "keyring") {
		t.Errorf("stdout should mention keyring entries: %q", out.String())
	}
}
