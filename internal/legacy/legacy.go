// Package legacy carries the on-disk state over from the project's old name.
//
// Until 0.10.0 the command was `tuta-cli`, and its config, cache and tmpfs
// paths were named after it. Migrate() moves that state to the `tuta-alias`
// names once, so an existing install keeps its session, saved `+` addresses,
// alias notes and config instead of starting from scratch.
//
// Every step is best-effort and silent: a migration that cannot run must never
// stop the CLI — the user then just gets a fresh config, which is recoverable.
package legacy

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tdwgm/tuta-alias/internal/atomicfile"
)

const (
	oldName = "tuta-cli"
	newName = "tuta-alias"
)

// Migrate moves the old per-user state to the new names. It is idempotent, and
// it never overwrites anything already stored under the new name.
func Migrate() {
	for _, base := range stateDirs() {
		mergeDir(filepath.Join(base, oldName), filepath.Join(base, newName))
	}
	renameConf()
}

// stateDirs returns the parent directories that hold a per-user state dir
// named after the command: config, cache and the tmpfs runtime dir.
//
// Each entry mirrors how the package that owns it resolves its own path, which
// is deliberately not uniform: config.Path() hardcodes ~/.config with no
// XDG_CONFIG_HOME override, session.dir() and api's version cache honour
// XDG_CACHE_HOME, and creds.rebootCachePath() honours XDG_RUNTIME_DIR. Getting
// this wrong would migrate a directory nothing reads and leave the real one
// behind, so keep these three in step with their owners.
func stateDirs() []string {
	var dirs []string
	home, err := os.UserHomeDir()

	if err == nil {
		dirs = append(dirs, filepath.Join(home, ".config"))
	}

	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		dirs = append(dirs, x)
	} else if err == nil {
		dirs = append(dirs, filepath.Join(home, ".cache"))
	}

	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		dirs = append(dirs, x)
	} else if uid := os.Getuid(); uid >= 0 {
		dirs = append(dirs, filepath.Join("/run", "user", strconv.Itoa(uid)))
	}

	return dirs
}

// mergeDir moves the contents of src into dst, entry by entry, and removes src
// once it is empty.
//
// It merges rather than renaming the directory wholesale because the new
// directory can exist without holding the state that matters. Measured
// 2026-10-26: running the Python test suite after the rename created
// ~/.cache/tuta-alias/ (tuta_alias/serve.py writes an api-token there), and a
// whole-directory rename then skipped the migration and left the real
// session.json behind in ~/.cache/tuta-cli/ — logging the user out, which is
// the one thing this package exists to prevent.
//
// Per entry, the new name still wins: an entry that already exists under the
// new name is never overwritten, it is just left in the old directory. That
// keeps the "newer state is never clobbered" rule where it belongs — on the
// files, not on their parent.
func mergeDir(src, dst string) {
	fi, err := os.Stat(src)
	if err != nil || !fi.IsDir() {
		return
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return
	}
	for _, e := range entries {
		target := filepath.Join(dst, e.Name())
		if _, err := os.Stat(target); err == nil {
			continue // something newer already lives under the new name
		}
		_ = os.Rename(filepath.Join(src, e.Name()), target)
	}
	// Only succeeds when every entry moved; a partial migration keeps the old
	// directory so the next run can finish it.
	_ = os.Remove(src)
}

// renameConf renames the config file itself inside the (already merged) config
// directory: tuta-cli.conf → tuta-alias.conf.
func renameConf() {
	dir := configDir()
	if dir == "" {
		return
	}
	src := filepath.Join(dir, oldName+".conf")
	dst := filepath.Join(dir, newName+".conf")
	if _, err := os.Stat(src); err != nil {
		return
	}
	if _, err := os.Stat(dst); err == nil {
		return
	}
	_ = os.Rename(src, dst)
}

// configDir mirrors config.Path()'s directory: ~/.config/tuta-alias, with no
// XDG_CONFIG_HOME override (config.Path() hardcodes it the same way).
func configDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", newName)
}

// RefreshComments rewrites the old name in the config file's COMMENT lines.
//
// config.Save is round-trip-aware and deliberately preserves comments, which
// is right for anything the user wrote — but it also means the header the old
// binary generated ("# tuta-cli configuration", "Bind address for tuta-cli-go
// serve") survives the rename forever. Values are never touched: only the
// text after a # on a comment-only line.
func RefreshComments(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(raw), "\n")
	changed := false
	for i, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue // a KEY="value" line; its value is the user's, not ours
		}
		for _, old := range []string{oldName + "-go", oldName} {
			if strings.Contains(l, old) {
				lines[i] = strings.ReplaceAll(lines[i], old, newName)
				changed = true
			}
		}
	}
	if !changed {
		return
	}
	// Same permissions the config is written with, and through the same
	// atomic write: a crash here used to be able to truncate the config.
	_ = atomicfile.Write(path, []byte(strings.Join(lines, "\n")), 0o600)
}
