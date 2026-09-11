package editor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// defaultMode is what a file raj creates gets. Only used when there is nothing
// on disk to copy the mode from.
const defaultMode os.FileMode = 0o644

// readFile is os.ReadFile under a name so a test can stub the read-back: a
// filesystem that accepts a write and drops the bytes cannot be conjured up on
// demand, so the test stands in for it.
var readFile = os.ReadFile

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
//
// The read-back at the end is the one guarantee the dance above cannot make on
// its own: a filesystem can report success for every call and still drop the
// bytes. Comparing what landed against what was sent is what makes a nil
// return mean the file is saved, rather than merely that nothing complained.
// The comparison is a full byte compare, not a hash: the files raj opens are
// small enough that hashing would add a step without saving one.
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

	// Everything above reporting success is the filesystem's word. Read the
	// file back and compare before returning nil: a mismatch means the write
	// was accepted and the bytes dropped anyway, and the save must fail
	// loudly here rather than mark the buffer clean over a lie.
	got, err := readFile(path)
	if err != nil {
		return fmt.Errorf("save written but could not be verified: reading back %s: %w", path, err)
	}
	if !bytes.Equal(got, data) {
		return fmt.Errorf("save verification failed: %s on disk does not match what was written (%d bytes read back, %d written)", path, len(got), len(data))
	}
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
