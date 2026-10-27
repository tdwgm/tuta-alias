// Package atomicfile writes a file so that a reader never sees a half-written
// one and a crash never leaves a truncated one.
//
// It exists because five packages had grown their own copy of the same dance
// (config, notes, plusalias, creds, session) and none of them called Sync, so
// "atomic" only covered a concurrent reader, not a power cut: the rename could
// be on disk while the bytes it points at were not. The config file and the
// session are exactly the files where that matters.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// afterClose runs between closing the temp file and renaming it, the one moment
// the file is reachable by its name and by nothing else. A test uses it to be
// somebody else who can write to the directory.
var afterClose = func(tmp string) {}

// Write creates path with perm, replacing anything already there.
//
// The data goes to a temp file in the same directory, is flushed to disk, and
// is then renamed over the target: rename within a directory is atomic, so a
// reader sees either the old file or the new one. The directory entry is
// flushed too, so the rename itself survives a crash. A symlink planted at
// path is replaced rather than followed.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename succeeded

	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	// CreateTemp makes the file 0600; widen or narrow it to what was asked
	// before it becomes visible under its real name. It is done on the open
	// file and not on tmp: a chmod by name, after the file is closed, lands on
	// whatever the name refers to by then, and follows a symlink put there by
	// anyone who can write to the directory. It comes before the Sync so that
	// the one flush covers the mode as well as the data.
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	afterClose(tmp)
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, path, err)
	}
	SyncDir(dir)
	return nil
}

// WriteMkdir is Write, creating the containing directory with dirPerm first.
func WriteMkdir(path string, data []byte, perm, dirPerm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return Write(path, data, perm)
}

// SyncDir flushes the directory entry so the rename itself is durable. Best
// effort: a filesystem that refuses to open a directory (or Windows) is not a
// reason to fail a write that already succeeded. It is exported for the one
// place that renames a file into place without going through Write, the
// installer, so that it does not need a second copy of this.
func SyncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}
