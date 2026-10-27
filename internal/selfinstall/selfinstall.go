// Package selfinstall is the one implementation of install and uninstall.
//
// There used to be two: internal/cliflow (for `install` / `uninstall`) and
// internal/tui (for the wizard and the Config screen's Uninstall row). They
// drifted, which is the worst kind of drift for this pair — the TUI wizard let
// the binary path be chosen while uninstall only ever looked in
// ~/.local/bin/tuta-alias, so a non-default install could not be removed, and
// neither side cleaned up the client-version cache. Everything that writes or
// removes an installed file now lives here.
package selfinstall

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/atomicfile"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/creds"
	"github.com/tdwgm/tuta-alias/internal/login"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/serve"
	"github.com/tdwgm/tuta-alias/internal/update"
)

// Apply installs what cfg asks for: the binary at cfg.BinaryPath() and the
// completion files whose COMPLETION_* key is true. It returns one log line per
// action taken. cfg is NOT written here; the caller owns the config file.
func Apply(cfg *config.Config, root *cobra.Command) ([]string, error) {
	var log []string

	dst := cfg.BinaryPath()
	if dst == "" {
		return nil, fmt.Errorf("cannot determine an install path (no home directory?)")
	}
	moved, err := CopySelf(dst)
	if err != nil {
		return log, err
	}
	if moved {
		log = append(log, "installed "+dst)
	} else {
		log = append(log, "already at "+dst)
	}

	for _, c := range []struct {
		on   bool
		path string
		gen  func(string) error
	}{
		{cfg.CompletionBash, config.BashCompletionPath(), func(p string) error { return root.GenBashCompletionFileV2(p, true) }},
		{cfg.CompletionZsh, config.ZshCompletionPath(), root.GenZshCompletionFile},
		{cfg.CompletionFish, config.FishCompletionPath(), func(p string) error { return root.GenFishCompletionFile(p, true) }},
	} {
		if !c.on || c.path == "" || root == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
			log = append(log, "completion: "+err.Error())
			continue
		}
		if err := c.gen(c.path); err != nil {
			log = append(log, "completion: "+err.Error())
			continue
		}
		log = append(log, "wrote "+c.path)
	}
	return log, nil
}

// Seams for tests.
var (
	// afterClose runs between closing the temp file and renaming it, the one
	// moment the file is reachable by its name and by nothing else.
	afterClose = func(tmpName string) {}
	// syncDir is how CopySelf flushes the directory once the rename is done.
	syncDir = atomicfile.SyncDir
)

// CopySelf copies the running binary to dst and reports whether it wrote
// anything (false = it is already there). The copy goes to a temp file in the
// target directory and is renamed into place: a plain WriteFile over the
// target fails with ETXTBSY while that binary is running, and a half-written
// executable is worse than none. Like internal/atomicfile, it flushes the file
// and then the directory entry, so a crash right after an install cannot leave
// the old binary, or none, behind a rename that reported success.
func CopySelf(dst string) (bool, error) {
	self, err := os.Executable()
	if err != nil {
		return false, err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if resolvedDst, err := filepath.EvalSymlinks(dst); err == nil && resolvedDst == self {
		return false, nil
	}
	if self == dst {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	in, err := os.Open(self)
	if err != nil {
		return false, err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+"-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }
	if _, err := io.Copy(tmp, in); err != nil {
		cleanup()
		return false, err
	}
	// On the open file and not on tmpName: a chmod by name, after the file is
	// closed, lands on whatever the name refers to by then, and follows a
	// symlink put there by anyone who can write to the directory. Before the
	// Sync, so that the one flush covers the mode as well as the bytes.
	if err := tmp.Chmod(0o755); err != nil {
		cleanup()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return false, err
	}
	afterClose(tmpName)
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return false, err
	}
	syncDir(filepath.Dir(dst))
	return true, nil
}

// Remove deletes the installed binary and completion files. With purge it
// also deletes the config directory (which holds the saved + addresses and
// alias notes as well), the session, the api-token, the client-version cache,
// the log and its rotation, the update check cache and the keyring entries.
// Every line it writes to w names what went.
func Remove(w io.Writer, purge bool) {
	// Read INSTALL_PATH, KEEPASS_DB and LOG_FILE before the config directory
	// goes: afterwards there is nothing left to say where the log was put.
	binPath, keePassDB, logFile := config.DefaultInstallPath(), "", ""
	if cfg, err := config.Load(); err == nil && cfg != nil {
		binPath, keePassDB, logFile = cfg.BinaryPath(), cfg.KeePassDB, cfg.LogFile
	}
	for _, p := range []string{
		binPath,
		config.BashCompletionPath(),
		config.ZshCompletionPath(),
		config.FishCompletionPath(),
	} {
		if p != "" {
			_ = os.Remove(p)
		}
	}
	fmt.Fprintln(w, "removed binary + completion files")
	if !purge {
		fmt.Fprintln(w, "kept: config, + addresses, alias notes, session, api-token, log, keyring entries")
		return
	}
	// filepath.Dir("") is ".", so testing the DIRECTORY for "" was dead code:
	// with no home directory a purge asked to remove the working directory,
	// and was saved only by the kernel refusing it.
	if cfgPath := config.Path(); cfgPath != "" {
		if dir := filepath.Dir(cfgPath); dir != "" && dir != "." && dir != string(filepath.Separator) {
			_ = os.RemoveAll(dir)
		}
	}
	_, _ = login.Logout()
	_ = os.Remove(serve.TokenPath())
	_ = os.Remove(api.ClientVersionPath())
	// The log is the one file that names what was done with the account: the
	// address logged in, every alias created or removed, the config values
	// that were changed. A purge that leaves it behind is not a purge, and
	// the rotation holds the same thing one generation back.
	logPath := logx.PathFor(logFile)
	if logPath != "" {
		_ = os.Remove(logPath)
		_ = os.Remove(logPath + ".1")
	}
	_ = os.Remove(update.CachePath())
	if err := creds.PurgeCaches(keePassDB); err != nil {
		fmt.Fprintln(w, "warning: keyring cleanup:", err)
	}
	// The cache directory itself, but only when this program's files were all
	// that was in it. os.Remove on a directory removes it only when it is
	// empty, which is exactly the condition wanted here: something else in
	// there is not ours to delete.
	if dir := cacheDir(); dir != "" {
		_ = os.Remove(dir)
	}
	fmt.Fprintln(w, "purged: config (incl. + addresses and alias notes), session, api-token, "+
		"client-version cache, log (and its .1 rotation), update check, keyring entries")
}

// cacheDir is where this program's cache files live, as logx and update
// resolve it. Only the directory itself is returned: what is inside it is
// removed by name, never recursively, because the directory is shared with
// whatever else the user keeps under ~/.cache/tuta-alias.
func cacheDir() string {
	p := update.CachePath()
	if p == "" {
		p = logx.DefaultPath()
	}
	if p == "" {
		return ""
	}
	return filepath.Dir(p)
}

// InPath reports whether dir is in $PATH.
func InPath(dir string) bool {
	if dir == "" {
		return false
	}
	abs, _ := filepath.Abs(dir)
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == "" {
			continue
		}
		// Clean both sides: a PATH entry written as /x/bin/ is the same
		// directory as /x/bin, and warning about it would be noise.
		p = filepath.Clean(p)
		if p == filepath.Clean(dir) || p == filepath.Clean(abs) {
			return true
		}
	}
	return false
}

// PathWarning returns the "not in $PATH" advice for the directory dst lives
// in, or "" when it is already there.
func PathWarning(dst string) string {
	dir := filepath.Dir(dst)
	// Absolute, because the line is meant to be pasted into a shell profile.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if dir == "" || InPath(dir) {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s is not in $PATH\n", dir)
	fmt.Fprintf(&b, "Add this to ~/.zshrc or ~/.bashrc:\n  %s\n", ExportPathLine(dir))
	return b.String()
}

// doubleQuoted escapes what a POSIX shell still interprets inside double
// quotes.
var doubleQuoted = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`")

// ExportPathLine is the line to paste into a shell profile to put dir in front
// of $PATH. The directory is part of a command the user is about to run, so
// what a shell still interprets inside double quotes is escaped: a name with a
// $ or a backquote in it would run as code or expand to something else, and
// one with a double quote would end the string early. A directory without
// any of those comes out as it always did, export PATH="<dir>:$PATH".
func ExportPathLine(dir string) string {
	return `export PATH="` + doubleQuoted.Replace(dir) + `:$PATH"`
}
