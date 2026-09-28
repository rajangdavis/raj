//go:build !linux

package git

import (
	"errors"
	"os"
)

// reflink has no portable spelling off Linux, so the caller does a full copy.
func reflink(*os.File, *os.File) error {
	return errors.New("git: reflink unsupported on this platform")
}
