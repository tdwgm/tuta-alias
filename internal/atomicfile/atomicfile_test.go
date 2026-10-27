package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// modeOf is the permission bits of the file at path.
func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// os.CreateTemp makes the file Write starts from 0600, so a test that asks for
// 0600 passes whether or not Write sets the mode at all. Every mode asked for
// here is one CreateTemp would not produce, and it is read back from the final
// path, after the rename, because that is the file a reader opens.
func TestWriteCreatesWithRequestedMode(t *testing.T) {
	// Measured, not assumed: the umask can change what CreateTemp gives.
	probe, err := os.CreateTemp(t.TempDir(), "probe-*")
	if err != nil {
		t.Fatal(err)
	}
	probe.Close()
	tempMode := modeOf(t, probe.Name())

	data := []byte("hello\n")
	write := func(dir string, perm os.FileMode) (string, error) {
		p := filepath.Join(dir, "file")
		return p, Write(p, data, perm)
	}
	writeMkdir := func(dir string, perm os.FileMode) (string, error) {
		p := filepath.Join(dir, "sub", "file")
		return p, WriteMkdir(p, data, perm, 0o700)
	}
	for _, tt := range []struct {
		name  string
		write func(dir string, perm os.FileMode) (string, error)
		perm  os.FileMode
	}{
		{name: "wider than the temp file", write: write, perm: 0o644},
		{name: "narrower than the temp file", write: write, perm: 0o400},
		// update installs the rebuilt binary with this mode: at the temp file's
		// mode it would replace the running tool with one that cannot be run.
		{name: "executable", write: write, perm: 0o755},
		// WriteMkdir creates the directory 0700, and the file must not follow it.
		{name: "file in a created directory keeps its own mode", write: writeMkdir, perm: 0o644},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.perm == tempMode {
				t.Fatalf("%v is the mode CreateTemp gives anyway, so this case cannot show that Write sets it", tt.perm)
			}
			p, err := tt.write(t.TempDir(), tt.perm)
			if err != nil {
				t.Fatal(err)
			}
			if raw, err := os.ReadFile(p); err != nil || string(raw) != string(data) {
				t.Fatalf("content = %q, %v", raw, err)
			}
			if got := modeOf(t, p); got != tt.perm {
				t.Errorf("mode = %v, want %v", got, tt.perm)
			}
		})
	}

	// Rename swaps in a new file, so the mode of what was there must not carry
	// over, in either direction: a file asked to be private must not stay
	// readable, and one asked to be readable must not stay private.
	for _, tt := range []struct {
		name string
		was  os.FileMode
		perm os.FileMode
	}{
		{name: "private replaces readable", was: 0o644, perm: 0o600},
		{name: "readable replaces private", was: 0o600, perm: 0o644},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(p, []byte("old"), tt.was); err != nil {
				t.Fatal(err)
			}
			// WriteFile is subject to the umask, and the file has to start
			// with exactly this mode.
			if err := os.Chmod(p, tt.was); err != nil {
				t.Fatal(err)
			}
			if err := Write(p, data, tt.perm); err != nil {
				t.Fatal(err)
			}
			if got := modeOf(t, p); got != tt.perm {
				t.Errorf("mode = %v, want %v", got, tt.perm)
			}
		})
	}
}

func TestWriteReplacesAndLeavesNoTempBehind(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "conf")
	if err := Write(p, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != "two" {
		t.Errorf("content = %q, want the second write", raw)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".conf-") {
			t.Errorf("a temp file was left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want just the file", len(entries))
	}
}

// A symlink at the target must be replaced, not followed: otherwise anything
// that can plant one decides where the config or the session is written.
func TestWriteReplacesASymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "link")
	if err := os.Symlink(victim, p); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
	if err := Write(p, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(victim); string(raw) != "untouched" {
		t.Errorf("the symlink was followed: victim = %q", raw)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("the target is still a symlink")
	}
}

// The mode is set on the open file. A chmod by name, after the file is closed,
// lands on whatever the name refers to by then, and the directory is where
// somebody else may have put a symlink, which chmod follows: the mode meant
// for the new file went to another one. Here the other party renames the temp
// file away and leaves a symlink to a private file in its place.
//
// What this does not show: the rename that follows is by name too, so the
// symlink is what ends up at the target. Nothing in the standard library
// renames by descriptor, and whoever can do this to the temp name can replace
// the target itself. The claim is only that the mode does not travel through
// the name.
func TestWriteSetsTheModeOnTheFileItWrote(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("not what this write is for"), 0o600); err != nil {
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
	afterClose = func(tmp string) {
		written = tmp + ".written"
		if err := os.Rename(tmp, written); err != nil {
			t.Error(err)
			return
		}
		linkErr = os.Symlink(victim, tmp)
	}

	if err := Write(filepath.Join(dir, "file"), []byte("data"), 0o755); err != nil {
		t.Fatal(err)
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

func TestWriteMkdirCreatesTheDirectory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "file")
	if err := WriteMkdir(p, []byte("x"), 0o600, 0o700); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("directory mode = %v, want 0700", got)
	}
}

func TestWriteFailsWhenTheDirectoryIsMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope", "file")
	if err := Write(p, []byte("x"), 0o600); err == nil {
		t.Error("Write into a missing directory should fail, not create it silently")
	}
}

// SyncDir is best effort, so a directory that cannot be opened is not a
// failure and not a panic: a write that already succeeded must not be undone
// by it.
func TestSyncDirToleratesADirectoryThatIsGone(t *testing.T) {
	SyncDir(t.TempDir())
	SyncDir(filepath.Join(t.TempDir(), "nope"))
}
