//go:build linux

package git

import (
	"os"

	"golang.org/x/sys/unix"
)

// reflink asks the filesystem to share extents between dst and src. It is
// best-effort: an unsupported filesystem returns an error and the caller falls
// back to a full copy. It never creates a hardlink -- the inodes stay separate
// even when the extents are shared -- so a build in the scratch tree cannot
// corrupt the source.
func reflink(dst, src *os.File) error {
	return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
}
