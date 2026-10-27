package legacy

import (
	"os"
	"path/filepath"
	"testing"
)

// sandbox points HOME and the XDG dirs at a temp tree so a test can never
// touch the real ~/.config/tuta-alias or ~/.cache/tuta-alias.
func sandbox(t *testing.T) (home, runtime string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	runtime = filepath.Join(root, "run")
	for _, d := range []string{home, runtime} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "ignored-config-home"))
	return home, runtime
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestMigrateMovesConfigCacheAndRuntime(t *testing.T) {
	home, runtime := sandbox(t)

	write(t, filepath.Join(home, ".config", "tuta-cli", "tuta-cli.conf"), "CRED_BACKEND=\"bitwarden\"\n")
	write(t, filepath.Join(home, ".config", "tuta-cli", "plus-aliases.json"), `{"a+x@example.com":{}}`)
	write(t, filepath.Join(home, ".cache", "tuta-cli", "session.json"), `{"user_id":"U1"}`)
	write(t, filepath.Join(runtime, "tuta-cli", "bw-session"), "cached")

	Migrate()

	if got := read(t, filepath.Join(home, ".config", "tuta-alias", "tuta-alias.conf")); got != "CRED_BACKEND=\"bitwarden\"\n" {
		t.Errorf("conf not carried over, got %q", got)
	}
	if got := read(t, filepath.Join(home, ".config", "tuta-alias", "plus-aliases.json")); got != `{"a+x@example.com":{}}` {
		t.Errorf("plus-aliases not carried over, got %q", got)
	}
	if got := read(t, filepath.Join(home, ".cache", "tuta-alias", "session.json")); got != `{"user_id":"U1"}` {
		t.Errorf("session not carried over, got %q", got)
	}
	if got := read(t, filepath.Join(runtime, "tuta-alias", "bw-session")); got != "cached" {
		t.Errorf("runtime cache not carried over, got %q", got)
	}

	// The old locations are gone, not duplicated.
	for _, p := range []string{
		filepath.Join(home, ".config", "tuta-cli"),
		filepath.Join(home, ".cache", "tuta-cli"),
		filepath.Join(runtime, "tuta-cli"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after migration", p)
		}
	}
}

// config.Path() hardcodes ~/.config and ignores XDG_CONFIG_HOME, so the
// migration must do the same — otherwise it moves a directory nothing reads.
func TestMigrateIgnoresXDGConfigHome(t *testing.T) {
	home, _ := sandbox(t)
	other := os.Getenv("XDG_CONFIG_HOME")

	write(t, filepath.Join(home, ".config", "tuta-cli", "tuta-cli.conf"), "A=1\n")
	write(t, filepath.Join(other, "tuta-cli", "tuta-cli.conf"), "B=2\n")

	Migrate()

	if got := read(t, filepath.Join(home, ".config", "tuta-alias", "tuta-alias.conf")); got != "A=1\n" {
		t.Errorf("~/.config was not migrated, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(other, "tuta-cli", "tuta-cli.conf")); err != nil {
		t.Errorf("XDG_CONFIG_HOME copy should have been left alone: %v", err)
	}
}

// A machine that already has the new directory keeps it; the old one is never
// allowed to overwrite newer state.
func TestMigrateDoesNotOverwriteExisting(t *testing.T) {
	home, _ := sandbox(t)

	write(t, filepath.Join(home, ".cache", "tuta-cli", "session.json"), `{"user_id":"OLD"}`)
	write(t, filepath.Join(home, ".cache", "tuta-alias", "session.json"), `{"user_id":"NEW"}`)
	write(t, filepath.Join(home, ".config", "tuta-alias", "tuta-cli.conf"), "OLD=1\n")
	write(t, filepath.Join(home, ".config", "tuta-alias", "tuta-alias.conf"), "NEW=1\n")

	Migrate()

	if got := read(t, filepath.Join(home, ".cache", "tuta-alias", "session.json")); got != `{"user_id":"NEW"}` {
		t.Errorf("existing session was overwritten, got %q", got)
	}
	if got := read(t, filepath.Join(home, ".config", "tuta-alias", "tuta-alias.conf")); got != "NEW=1\n" {
		t.Errorf("existing conf was overwritten, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".cache", "tuta-cli", "session.json")); err != nil {
		t.Errorf("old session should be left in place when the new one exists: %v", err)
	}
}

// Regression, measured 2026-10-26: the Python test suite had already created
// ~/.cache/tuta-alias/ (serve.py writes an api-token there), and a
// whole-directory rename then skipped the migration entirely and left
// session.json in ~/.cache/tuta-cli/ — the user was effectively logged out.
// An existing but unrelated new directory must not block the migration.
func TestMigrateMergesIntoExistingNewDir(t *testing.T) {
	home, _ := sandbox(t)

	write(t, filepath.Join(home, ".cache", "tuta-cli", "session.json"), `{"user_id":"U1"}`)
	write(t, filepath.Join(home, ".cache", "tuta-cli", "client-version"), "361.260929.0")
	write(t, filepath.Join(home, ".cache", "tuta-alias", "api-token"), "from-the-test-suite")

	Migrate()

	if got := read(t, filepath.Join(home, ".cache", "tuta-alias", "session.json")); got != `{"user_id":"U1"}` {
		t.Errorf("session.json was not merged in, got %q", got)
	}
	if got := read(t, filepath.Join(home, ".cache", "tuta-alias", "client-version")); got != "361.260929.0" {
		t.Errorf("client-version was not merged in, got %q", got)
	}
	if got := read(t, filepath.Join(home, ".cache", "tuta-alias", "api-token")); got != "from-the-test-suite" {
		t.Errorf("pre-existing file was disturbed, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".cache", "tuta-cli")); !os.IsNotExist(err) {
		t.Error("emptied old directory should have been removed")
	}
}

// A per-entry collision keeps the new file and leaves the old one behind, so
// the old directory survives for inspection instead of being half-deleted.
func TestMigrateKeepsOldDirOnEntryCollision(t *testing.T) {
	home, _ := sandbox(t)

	write(t, filepath.Join(home, ".cache", "tuta-cli", "session.json"), `{"user_id":"OLD"}`)
	write(t, filepath.Join(home, ".cache", "tuta-cli", "client-version"), "348.260506.0")
	write(t, filepath.Join(home, ".cache", "tuta-alias", "session.json"), `{"user_id":"NEW"}`)

	Migrate()

	if got := read(t, filepath.Join(home, ".cache", "tuta-alias", "session.json")); got != `{"user_id":"NEW"}` {
		t.Errorf("newer session.json was overwritten, got %q", got)
	}
	if got := read(t, filepath.Join(home, ".cache", "tuta-alias", "client-version")); got != "348.260506.0" {
		t.Errorf("non-colliding entry should still migrate, got %q", got)
	}
	if got := read(t, filepath.Join(home, ".cache", "tuta-cli", "session.json")); got != `{"user_id":"OLD"}` {
		t.Errorf("colliding entry should stay in the old dir, got %q", got)
	}
}

// Nothing to migrate, and a second run after a successful one, must both be
// no-ops rather than errors.
func TestMigrateIdempotentAndEmpty(t *testing.T) {
	home, _ := sandbox(t)

	Migrate() // nothing exists at all

	write(t, filepath.Join(home, ".cache", "tuta-cli", "session.json"), `{"user_id":"U1"}`)
	Migrate()
	Migrate()

	if got := read(t, filepath.Join(home, ".cache", "tuta-alias", "session.json")); got != `{"user_id":"U1"}` {
		t.Errorf("second Migrate disturbed the result, got %q", got)
	}
}
