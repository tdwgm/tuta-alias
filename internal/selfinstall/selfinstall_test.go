package selfinstall

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	keyringlib "github.com/zalando/go-keyring"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/creds"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/notes"
	"github.com/tdwgm/tuta-alias/internal/plusalias"
	"github.com/tdwgm/tuta-alias/internal/serve"
	"github.com/tdwgm/tuta-alias/internal/session"
	"github.com/tdwgm/tuta-alias/internal/update"
)

// sandbox points every path this package touches (config, completions, cache,
// reboot cache) at a temp tree and swaps the keyring for the in-memory mock.
// Remove(purge) deletes real files and calls the keyring; without this a test
// run would wipe the developer's own installation and saved login.
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	keyringlib.MockInit()
	return home
}

// testRoot is the smallest cobra tree the completion generators accept.
func testRoot() *cobra.Command {
	root := &cobra.Command{Use: "tuta-alias"}
	root.AddCommand(&cobra.Command{Use: "list", Run: func(*cobra.Command, []string) {}})
	return root
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
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

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func selfBytes(t *testing.T) []byte {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// CopySelf is the only code that writes the installed binary. It must produce
// an exact, executable copy, create the target directory, leave no ".name-*"
// temp file behind, and copy again over an existing file: re-running `install`
// after an upgrade has to replace the old binary, not skip it because
// something is already there. Two real copies of the test binary are made in
// this one test to keep the cost down.
func TestCopySelfFreshAndOverwrite(t *testing.T) {
	sandbox(t)
	dir := filepath.Join(t.TempDir(), "does", "not", "exist")
	dst := filepath.Join(dir, "tuta-alias")
	want := selfBytes(t)

	moved, err := CopySelf(dst)
	if err != nil || !moved {
		t.Fatalf("first CopySelf = %v, %v; want true, nil", moved, err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755 (a non-executable install is useless)", st.Mode().Perm())
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, want) {
		t.Errorf("installed copy differs from the running executable (%d vs %d bytes)", len(got), len(want))
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "tuta-alias" {
		t.Errorf("target dir should hold only the binary, got %v", entries)
	}

	// An old, different file at dst: a content-based "already installed" check
	// would leave it there and the upgrade would silently not happen.
	if err := os.WriteFile(dst, []byte("old version"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved, err = CopySelf(dst)
	if err != nil || !moved {
		t.Fatalf("second CopySelf = %v, %v; want true, nil", moved, err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, want) {
		t.Error("second CopySelf did not replace the stale file")
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0o755 {
		t.Errorf("mode after overwrite = %v, want 0755", st.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("temp file left behind after overwrite: %v", entries)
	}
}

// `install` says "already at ..." when run from the installed binary. If
// CopySelf copied a file onto itself (directly or through a symlink) it would
// either truncate the running executable or replace a symlink with a regular
// file; both are worse than doing nothing.
func TestCopySelfIsNoopOnTheRunningExecutable(t *testing.T) {
	sandbox(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(self)
	if err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(t.TempDir(), "tuta-alias")
	if err := os.Symlink(self, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	for name, dst := range map[string]string{"direct path": self, "symlink to it": link} {
		moved, err := CopySelf(dst)
		if err != nil || moved {
			t.Errorf("%s: CopySelf = %v, %v; want false, nil", name, moved, err)
		}
	}
	after, err := os.Stat(self)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Error("the running executable was modified")
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced by a regular file: %v, %v", fi, err)
	}
}

// A destination that cannot be created must come back as an error with
// moved=false: Apply turns that into the "install failed" message, and a true
// here would print "installed" for a binary that is not there.
func TestCopySelfReportsUnwritableTarget(t *testing.T) {
	sandbox(t)
	blocker := filepath.Join(t.TempDir(), "file")
	mustWrite(t, blocker, "not a directory")

	moved, err := CopySelf(filepath.Join(blocker, "sub", "tuta-alias"))
	if err == nil || moved {
		t.Errorf("CopySelf = %v, %v; want false and an error", moved, err)
	}
}

// The mode goes on the open temp file. A chmod by name, after the file is
// closed, lands on whatever the name refers to by then, and a symlink put
// there is followed: the 0755 meant for the new binary went to another file.
// The path to a real failure is `sudo tuta-alias install` with HOME kept,
// which installs as root into a directory the invoking user can write, where
// the file that gets chmod-ed can be any file of root's.
//
// What this does not show: the rename is by name too, so the symlink is what
// ends up at dst, and whoever can write to that directory can replace the
// binary anyway. The claim is only that the mode does not travel through the
// name.
func TestCopySelfSetsTheModeOnTheFileItWrote(t *testing.T) {
	sandbox(t)
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("not what the install is for"), 0o600); err != nil {
		t.Fatal(err)
	}
	// WriteFile is subject to the umask, and the file has to be exactly 0600.
	if err := os.Chmod(victim, 0o600); err != nil {
		t.Fatal(err)
	}

	var written string
	var linkErr error
	prev := afterClose
	t.Cleanup(func() { afterClose = prev })
	afterClose = func(tmpName string) {
		written = tmpName + ".written"
		if err := os.Rename(tmpName, written); err != nil {
			t.Error(err)
			return
		}
		linkErr = os.Symlink(victim, tmpName)
	}

	moved, err := CopySelf(filepath.Join(dir, "bin", "tuta-alias"))
	if err != nil || !moved {
		t.Fatalf("CopySelf = %v, %v; want true, nil", moved, err)
	}
	if linkErr != nil {
		t.Skipf("symlinks not supported here: %v", linkErr)
	}
	if written == "" {
		t.Fatal("the seam did not run, so nothing was tested")
	}
	if got := modeOf(t, victim); got != 0o600 {
		t.Errorf("the file the temp name pointed to by then has mode %04o, want 0600: the mode was applied through the name", got)
	}
	if got := modeOf(t, written); got != 0o755 {
		t.Errorf("the file that was written has mode %04o, want 0755", got)
	}
}

// The rename has to reach the disk as well as the bytes: without a flush of
// the directory a crash right after `install` can lose the entry, and the next
// boot has the old binary or none. It is flushed after the rename and not
// before, or the entry being flushed is still the old one. A copy that did not
// happen has nothing to flush.
func TestCopySelfSyncsTheDirectoryAfterTheRename(t *testing.T) {
	sandbox(t)
	dst := filepath.Join(t.TempDir(), "bin", "tuta-alias")

	var synced []string
	var installedThen bool
	prev := syncDir
	t.Cleanup(func() { syncDir = prev })
	syncDir = func(dir string) {
		synced = append(synced, dir)
		installedThen = exists(dst)
	}

	moved, err := CopySelf(dst)
	if err != nil || !moved {
		t.Fatalf("CopySelf = %v, %v; want true, nil", moved, err)
	}
	if len(synced) != 1 || synced[0] != filepath.Dir(dst) {
		t.Fatalf("directories synced = %q, want just %q", synced, filepath.Dir(dst))
	}
	if !installedThen {
		t.Error("the directory was synced before the binary was renamed into it")
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	synced = nil
	if moved, err := CopySelf(self); err != nil || moved {
		t.Fatalf("CopySelf onto the running binary = %v, %v; want false, nil", moved, err)
	}
	if len(synced) != 0 {
		t.Errorf("directories synced for a copy that did not happen = %q", synced)
	}
}

// Apply writes exactly the completion files whose COMPLETION_* key is on. A
// regression that ignored a key would drop a file into ~/.config/fish for a
// user who never asked for fish.
func TestApplyWritesOnlyEnabledCompletions(t *testing.T) {
	for _, c := range []struct {
		name             string
		bash, zsh, fish  bool
		wantBash, wantZ  bool
		wantFish         bool
		wantCompletionLn int
	}{
		{name: "none"},
		{name: "bash only", bash: true, wantBash: true, wantCompletionLn: 1},
		{name: "zsh only", zsh: true, wantZ: true, wantCompletionLn: 1},
		{name: "fish only", fish: true, wantFish: true, wantCompletionLn: 1},
		{name: "all", bash: true, zsh: true, fish: true, wantBash: true, wantZ: true, wantFish: true, wantCompletionLn: 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			sandbox(t)
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			// Installing to the running binary makes CopySelf a no-op, so this
			// table does not copy the test binary five times.
			cfg := config.Default()
			cfg.InstallPath = self
			cfg.CompletionBash, cfg.CompletionZsh, cfg.CompletionFish = c.bash, c.zsh, c.fish

			log, err := Apply(cfg, testRoot())
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			for path, want := range map[string]bool{
				config.BashCompletionPath(): c.wantBash,
				config.ZshCompletionPath():  c.wantZ,
				config.FishCompletionPath(): c.wantFish,
			} {
				if got := exists(path); got != want {
					t.Errorf("%s exists = %v, want %v", path, got, want)
				}
				if want {
					if b, _ := os.ReadFile(path); len(b) == 0 {
						t.Errorf("%s is empty", path)
					}
				}
			}
			// One line for the binary plus one per completion file.
			if len(log) != 1+c.wantCompletionLn {
				t.Errorf("log = %q, want %d lines", log, 1+c.wantCompletionLn)
			}
			if len(log) > 0 && log[0] != "already at "+self {
				t.Errorf("log[0] = %q", log[0])
			}
		})
	}
}

// The log is what `install` and the wizard show the user: it must say
// "installed <path>" when the binary was written, and name each completion
// file it wrote.
func TestApplyLogsEachAction(t *testing.T) {
	home := sandbox(t)
	cfg := config.Default()
	cfg.InstallPath = filepath.Join(home, "opt", "bin", "tuta-alias")
	cfg.CompletionBash, cfg.CompletionZsh, cfg.CompletionFish = true, false, true

	log, err := Apply(cfg, testRoot())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := []string{
		"installed " + cfg.InstallPath,
		"wrote " + config.BashCompletionPath(),
		"wrote " + config.FishCompletionPath(),
	}
	if strings.Join(log, "\n") != strings.Join(want, "\n") {
		t.Errorf("log = %q\nwant  %q", log, want)
	}
	if st, err := os.Stat(cfg.InstallPath); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("binary at INSTALL_PATH missing or not 0755: %v, %v", st, err)
	}
	if exists(config.ZshCompletionPath()) {
		t.Error("zsh completion written although COMPLETION_ZSH is false")
	}
}

// Without a home directory there is no install path; Apply has to say so
// instead of copying the binary to "" or to the current directory. An
// INSTALL_PATH that cannot be written must also surface as an error, because
// the caller prints it as the reason the install failed.
func TestApplyErrors(t *testing.T) {
	t.Run("no install path", func(t *testing.T) {
		sandbox(t)
		t.Setenv("HOME", "")
		cfg := config.Default()
		log, err := Apply(cfg, testRoot())
		if err == nil {
			t.Fatalf("Apply with no home = nil error, log %q", log)
		}
		if len(log) != 0 {
			t.Errorf("log = %q, want none", log)
		}
	})
	t.Run("unwritable install path", func(t *testing.T) {
		home := sandbox(t)
		blocker := filepath.Join(home, "blocker")
		mustWrite(t, blocker, "a file where a directory is needed")
		cfg := config.Default()
		cfg.InstallPath = filepath.Join(blocker, "tuta-alias")
		cfg.CompletionBash = true

		log, err := Apply(cfg, testRoot())
		if err == nil {
			t.Fatalf("Apply = nil error, log %q", log)
		}
		if exists(config.BashCompletionPath()) {
			t.Error("completion written although the binary install failed")
		}
	})
}

// A completion file that cannot be written is reported in the log and does not
// stop the others or fail the install: the binary is already in place, and a
// broken ~/.config/zsh must not make `install` exit non-zero.
func TestApplyCompletionFailureIsLoggedNotFatal(t *testing.T) {
	home := sandbox(t)
	self, _ := os.Executable()
	cfg := config.Default()
	cfg.InstallPath = self
	cfg.CompletionBash, cfg.CompletionZsh, cfg.CompletionFish = true, true, false
	// ~/.local/share is a file, so the bash completion directory cannot exist.
	mustWrite(t, filepath.Join(home, ".local", "share"), "in the way")

	log, err := Apply(cfg, testRoot())
	if err != nil {
		t.Fatalf("Apply = %v, want nil", err)
	}
	var failed, wroteZsh bool
	for _, l := range log {
		failed = failed || strings.HasPrefix(l, "completion: ")
		wroteZsh = wroteZsh || l == "wrote "+config.ZshCompletionPath()
	}
	if !failed {
		t.Errorf("no \"completion: ...\" line in %q", log)
	}
	if !wroteZsh || !exists(config.ZshCompletionPath()) {
		t.Errorf("zsh completion should still be written; log %q", log)
	}
}

// Apply guards root == nil (the wizard can call it before the command tree
// exists). It must still install the binary and not dereference nil.
func TestApplyWithNilRoot(t *testing.T) {
	sandbox(t)
	self, _ := os.Executable()
	cfg := config.Default()
	cfg.InstallPath = self
	cfg.CompletionBash, cfg.CompletionZsh, cfg.CompletionFish = true, true, true

	log, err := Apply(cfg, nil)
	if err != nil || len(log) != 1 {
		t.Errorf("Apply(nil root) = %q, %v; want just the binary line", log, err)
	}
	if exists(config.BashCompletionPath()) {
		t.Error("completion written without a command tree")
	}
}

// fixture is a complete installation inside the sandbox: binary, completions,
// config, plus-aliases, alias notes, session, api-token, client-version cache
// and a saved login in the (mock) keyring. It is built through the packages
// that own each file so the paths Remove deletes are the paths they write.
type fixture struct {
	cfg        *config.Config
	binary     string
	completion []string
	configDir  string
	// keepOnNoPurge are the files a plain uninstall must leave.
	keepOnNoPurge []string
}

func install(t *testing.T, installPath string) fixture {
	t.Helper()
	cfg := config.Default()
	cfg.InstallPath = installPath
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := plusalias.Save([]plusalias.Entry{{Address: "me+x@example.com", Note: "n", Created: "2026-01-01"}}); err != nil {
		t.Fatal(err)
	}
	if err := notes.Save(map[string]string{"a@example.com": "note"}); err != nil {
		t.Fatal(err)
	}
	// No AccessToken: login.Logout would otherwise try to close the session on
	// the Tuta server, and tests do not use the network.
	if _, err := session.Save(&session.Session{MailAddress: "me@example.com", Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := serve.GetOrCreateToken(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, api.ClientVersionPath(), "1.2.3")
	if err := creds.SaveLogin("me@example.com", "pw"); err != nil {
		t.Fatal(err)
	}

	f := fixture{
		cfg:        cfg,
		binary:     cfg.BinaryPath(),
		configDir:  filepath.Dir(config.Path()),
		completion: []string{config.BashCompletionPath(), config.ZshCompletionPath(), config.FishCompletionPath()},
	}
	mustWrite(t, f.binary, "binary")
	for _, p := range f.completion {
		mustWrite(t, p, "completion")
	}
	f.keepOnNoPurge = []string{
		config.Path(), plusalias.Path(), notes.Path(),
		session.Path(), serve.TokenPath(), api.ClientVersionPath(),
	}
	for _, p := range f.keepOnNoPurge {
		if !exists(p) {
			t.Fatalf("fixture did not create %s", p)
		}
	}
	return f
}

// A plain uninstall removes the program and nothing the user made. The kept
// files are asserted present, not just the removed ones absent, because "delete
// less" and "delete everything" both look fine if only one side is checked.
func TestRemoveKeepsUserData(t *testing.T) {
	sandbox(t)
	f := install(t, config.DefaultInstallPath())

	var out bytes.Buffer
	Remove(&out, false)

	for _, p := range append([]string{f.binary}, f.completion...) {
		if exists(p) {
			t.Errorf("%s still exists after uninstall", p)
		}
	}
	for _, p := range f.keepOnNoPurge {
		if !exists(p) {
			t.Errorf("%s was deleted by a non-purge uninstall", p)
		}
	}
	if _, err := creds.LoadLogin(); err != nil {
		t.Errorf("saved login was removed by a non-purge uninstall: %v", err)
	}
	if !strings.Contains(out.String(), "removed") || !strings.Contains(out.String(), "kept") {
		t.Errorf("output does not say what happened: %q", out.String())
	}
	if strings.Contains(out.String(), "purged") {
		t.Errorf("non-purge output claims a purge: %q", out.String())
	}
}

// --purge is the only thing allowed to delete the config directory, the
// + addresses, the notes, the session, the api-token, the client-version
// cache and the saved login. Each is asserted separately: a purge that forgot
// one (as the original did with the client-version cache) must fail here.
func TestRemovePurgeDeletesEverything(t *testing.T) {
	sandbox(t)
	f := install(t, config.DefaultInstallPath())

	var out bytes.Buffer
	Remove(&out, true)

	for _, p := range append([]string{f.binary}, f.completion...) {
		if exists(p) {
			t.Errorf("%s still exists after purge", p)
		}
	}
	for _, p := range f.keepOnNoPurge {
		if exists(p) {
			t.Errorf("%s survived a purge", p)
		}
	}
	if exists(f.configDir) {
		t.Errorf("config directory %s survived a purge", f.configDir)
	}
	if _, err := creds.LoadLogin(); !errors.Is(err, creds.ErrNoSavedLogin) {
		t.Errorf("saved login after purge: err = %v, want ErrNoSavedLogin", err)
	}
	if !strings.Contains(out.String(), "purged") {
		t.Errorf("output does not say it purged: %q", out.String())
	}
}

// This is the bug the package exists for: uninstall used to look only in
// ~/.local/bin and so could not remove a binary the wizard had put elsewhere.
// With purge, the same holds because INSTALL_PATH lives in the config file that
// purge deletes: reading it after the config directory is gone would leave the
// binary behind.
func TestRemoveHonoursInstallPath(t *testing.T) {
	for _, purge := range []bool{false, true} {
		name := "keep data"
		if purge {
			name = "purge"
		}
		t.Run(name, func(t *testing.T) {
			home := sandbox(t)
			custom := filepath.Join(home, "opt", "tools", "tuta-alias")
			f := install(t, custom)
			if f.binary != custom {
				t.Fatalf("fixture binary = %s, want %s", f.binary, custom)
			}

			Remove(&bytes.Buffer{}, purge)

			if exists(custom) {
				t.Errorf("binary at INSTALL_PATH %s was not removed (purge=%v)", custom, purge)
			}
		})
	}
}

// Uninstall on a machine that never had the program must be quiet and safe:
// the user may run it twice, or after deleting things by hand. It still has to
// print what it did, so the user is not left with an empty terminal.
func TestRemoveWithNothingInstalled(t *testing.T) {
	for _, purge := range []bool{false, true} {
		sandbox(t)
		var out bytes.Buffer
		Remove(&out, purge)
		if out.Len() == 0 {
			t.Errorf("purge=%v: Remove printed nothing", purge)
		}
		if !strings.Contains(out.String(), "removed") {
			t.Errorf("purge=%v: output %q does not say what it did", purge, out.String())
		}
		if purge && !strings.Contains(out.String(), "purged") {
			t.Errorf("purge output %q lacks the purge line", out.String())
		}
	}
}

// If the config file cannot be read, Remove falls back to the default install
// path rather than giving up: a broken config must not make uninstall a no-op.
func TestRemoveFallsBackToDefaultPathWhenConfigUnreadable(t *testing.T) {
	sandbox(t)
	// A directory where the file should be makes ReadFile fail with something
	// other than "not exist", which is the one case Load reports as an error.
	if err := os.MkdirAll(config.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, config.DefaultInstallPath(), "binary")

	Remove(&bytes.Buffer{}, false)

	if exists(config.DefaultInstallPath()) {
		t.Error("default binary was not removed when the config was unreadable")
	}
}

// With no home directory config.Path() is "". filepath.Dir("") is ".", so a
// purge that trusted it would aim os.RemoveAll at the current directory.
// (RemoveAll refuses "." today, which is luck; this pins the outcome.)
func TestRemovePurgeWithoutHomeLeavesCwdAlone(t *testing.T) {
	sandbox(t)
	t.Setenv("HOME", "")
	cwd := t.TempDir()
	sentinel := filepath.Join(cwd, "sentinel")
	mustWrite(t, sentinel, "keep me")
	t.Chdir(cwd)

	Remove(&bytes.Buffer{}, true)

	if !exists(sentinel) {
		t.Error("purge without a home directory deleted files in the current directory")
	}
}

// InPath compares against $PATH both as given and made absolute, so a
// relative directory that is on the path in absolute form counts.
func TestInPath(t *testing.T) {
	base := t.TempDir()
	in := filepath.Join(base, "in")
	other := filepath.Join(base, "other")
	for _, d := range []string{in, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", strings.Join([]string{"/usr/bin", in}, string(os.PathListSeparator)))
	t.Chdir(base)

	for _, c := range []struct {
		dir  string
		want bool
	}{
		{in, true},
		{"in", true}, // relative, resolves to an entry
		{other, false},
		{"other", false},
		{"/usr/bin", true},
	} {
		if got := InPath(c.dir); got != c.want {
			t.Errorf("InPath(%q) = %v, want %v", c.dir, got, c.want)
		}
	}

	t.Setenv("PATH", "")
	if InPath(in) {
		t.Error("InPath true with an empty PATH")
	}
}

// The advice is meant to be pasted into a shell rc file. A "~" inside double
// quotes does not expand, so the exported directory has to be the absolute one
// or the PATH entry silently points nowhere.
func TestPathWarning(t *testing.T) {
	base := t.TempDir()
	onPath := filepath.Join(base, "onpath")
	offPath := filepath.Join(base, "offpath")
	t.Setenv("PATH", "/usr/bin"+string(os.PathListSeparator)+onPath)

	if got := PathWarning(filepath.Join(onPath, "tuta-alias")); got != "" {
		t.Errorf("PathWarning for a dir in PATH = %q, want empty", got)
	}

	got := PathWarning(filepath.Join(offPath, "tuta-alias"))
	if got == "" {
		t.Fatal("PathWarning for a dir outside PATH is empty")
	}
	if !strings.Contains(got, offPath+" is not in $PATH") {
		t.Errorf("advice does not name the directory: %q", got)
	}
	export := `export PATH="` + offPath + `:$PATH"`
	if !strings.Contains(got, export) {
		t.Errorf("advice lacks the copy-pasteable line %s\n%q", export, got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "export PATH=") && strings.Contains(line, "~") {
			t.Errorf("export line uses a tilde, which does not expand inside quotes: %q", line)
		}
	}
}

// The directory is part of a command the user pastes, and inside double quotes
// a shell still expands $ and backquotes and ends the string at a quote. A
// directory with one of them in its name ran as code, or pointed the PATH
// entry somewhere else: `$HOME` in a name expanded to the home directory. The
// line is run here by a real shell, because what it has to do is put exactly
// this directory in front of PATH and run nothing.
func TestPathWarningIsSafeToPasteIntoAShell(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh to run the advice with: %v", err)
	}
	for _, c := range []struct{ name, dir string }{
		{"command substitution", "BASE/$(touch MARKER)/bin"},
		{"backquotes", "BASE/`touch MARKER`/bin"},
		{"a variable", "BASE/$HOME/bin"},
		{"a double quote", `BASE/say "hi"/bin`},
		{"a backslash", `BASE/a\b/bin`},
		{"a backslash before a dollar", `BASE/a\$b/bin`},
		{"a space", "BASE/my tools/bin"},
		{"a single quote", "BASE/it's/bin"},
	} {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			marker := filepath.Join(base, "marker")
			dir := strings.NewReplacer("BASE", base, "MARKER", marker).Replace(c.dir)
			t.Setenv("PATH", "/usr/bin")

			var line string
			for l := range strings.SplitSeq(PathWarning(filepath.Join(dir, "tuta-alias")), "\n") {
				if strings.Contains(l, "export PATH=") {
					line = strings.TrimSpace(l)
				}
			}
			if line == "" {
				t.Fatal("the advice has no export line")
			}

			cmd := exec.Command(shell, "-c", line+`; printf %s "$PATH"`)
			cmd.Env = []string{"PATH=/usr/bin:/bin"}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("the shell refused %s: %v\n%s", line, err, out)
			}
			if want := dir + ":/usr/bin:/bin"; string(out) != want {
				t.Errorf("%s\nleaves PATH = %q, want %q", line, out, want)
			}
			if exists(marker) {
				t.Errorf("pasting %s ran a command", line)
			}
		})
	}
}

// A purge is advertised as removing all local state, and the log is the file
// that names what was done with the account: the address logged in, every
// alias created or removed, the config values that were changed. It, its .1
// rotation and the update check cache were all left behind.
func TestRemovePurgeDeletesTheLogAndTheUpdateCache(t *testing.T) {
	sandbox(t)
	install(t, config.DefaultInstallPath())

	logPath := logx.DefaultPath()
	write(t, logPath, "2026-10-27 10:00:00 INFO logged in as u@example.com\n")
	write(t, logPath+".1", "2026-10-27 09:00:00 INFO created spam-1a2b@example.com\n")
	write(t, update.CachePath(), `{"ref":"main","remote":"abc","checked_at":"2026-10-27T10:00:00Z"}`)

	var out bytes.Buffer
	Remove(&out, true)

	for _, p := range []string{logPath, logPath + ".1", update.CachePath()} {
		if exists(p) {
			t.Errorf("%s survived a purge", p)
		}
	}
	if dir := filepath.Dir(logPath); exists(dir) {
		t.Errorf("the cache directory %s survived a purge with nothing left in it", dir)
	}
	// Only this program's directory. $XDG_CACHE_HOME itself holds everyone
	// else's caches and is never ours to remove.
	if root := filepath.Dir(filepath.Dir(logPath)); !exists(root) {
		t.Errorf("the purge removed %s, which is XDG_CACHE_HOME and not this program's", root)
	}
	for _, want := range []string{"log", "update check"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the report does not mention %q: %q", want, out.String())
		}
	}
}

// LOG_FILE can point anywhere. The file is still this program's and goes with
// a purge, but the directory the user chose for it is not ours to remove.
func TestRemovePurgeFollowsLogFileOutOfTheCacheDirectory(t *testing.T) {
	home := sandbox(t)
	elsewhere := filepath.Join(home, "logs", "tuta.log")
	cfg := config.Default()
	cfg.LogFile = elsewhere
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	write(t, elsewhere, "2026-10-27 10:00:00 INFO logged in as u@example.com\n")
	write(t, elsewhere+".1", "older\n")

	Remove(&bytes.Buffer{}, true)

	if exists(elsewhere) || exists(elsewhere+".1") {
		t.Errorf("the log at LOG_FILE %s survived a purge", elsewhere)
	}
	if !exists(filepath.Dir(elsewhere)) {
		t.Errorf("the directory %s was removed, and it is not this program's", filepath.Dir(elsewhere))
	}
}

// Something else under ~/.cache/tuta-alias is not ours to delete, so the
// directory only goes when this program's own files were all that was in it.
func TestRemovePurgeKeepsAForeignFileInTheCacheDirectory(t *testing.T) {
	sandbox(t)
	logPath := logx.DefaultPath()
	write(t, logPath, "log\n")
	foreign := filepath.Join(filepath.Dir(logPath), "someone-elses.json")
	write(t, foreign, "{}\n")

	Remove(&bytes.Buffer{}, true)

	if exists(logPath) {
		t.Errorf("%s survived a purge", logPath)
	}
	if !exists(foreign) {
		t.Errorf("%s was removed, and this program did not create it", foreign)
	}
}

// write creates a file and the directories above it.
func write(t *testing.T, path, content string) {
	t.Helper()
	if path == "" {
		t.Fatal("empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
