package editor

import (
	"os"
	"path/filepath"
)

// defaultMode is what a file raj creates gets. Only used when there is nothing
// on disk to copy the mode from.
const defaultMode os.FileMode = 0o644

// writeAtomic replaces path's contents with data, or leaves the file exactly as
// it was.
//
// os.WriteFile truncates first and then writes, so a crash, a full disk or a
// killed process between the two leaves a file that is neither the old contents
// nor the new — usually a zero-byte one, which is the worst possible outcome
// for a save. Writing a sibling temp file and renaming over the target makes
// the replacement a single atomic operation: any interruption leaves the
// previous file intact and at most a stray temp file behind.
//
// Three details the obvious version gets wrong:
//
//   - The temp file goes in the target's own directory. A rename across
//     filesystems fails, and /tmp is a different filesystem often enough that
//     saving would break exactly on the machines that separate them.
//   - The mode is copied from the file being replaced. A fresh temp file is
//     0600, so without this every save would quietly strip the group and other
//     bits off an executable script.
//   - Symlinks are resolved first. Renaming onto a symlink replaces the link
//     with a regular file, so editing a dotfile symlinked into a repository
//     would break the link on the first save and leave the real file stale.
//
// The fsync is what makes the guarantee hold across a power loss rather than
// only across a process death: without it the rename can reach the disk before
// the data it points at.
func writeAtomic(path string, data []byte) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	mode := defaultMode
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".raj-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// Every failure past this point removes the temp file. A save that fails
	// should not litter the directory it failed in — and the user is about to
	// try again.
	defer os.Remove(name)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir flushes the directory entry the rename created. Best effort: some
// filesystems refuse to open a directory for sync, and a durable file under a
// not-yet-durable name is still better than what came before.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	d.Sync()
	d.Close()
}
