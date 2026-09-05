//go:build smoke && linux

package smoke

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// openPTY returns a connected master/slave pair.
//
// Written against the ioctls rather than pulled in as a dependency: raj has two
// modules in go.mod and neither is a test-only convenience, which is a property
// worth keeping for forty lines of syscall.
const (
	tiocSPTLCK = 0x40045431 // unlock the slave
	tiocGPTN   = 0x80045430 // slave's number
)

func openPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	var unlock int32
	if err := ioctl(m.Fd(), tiocSPTLCK, uintptr(unsafe.Pointer(&unlock))); err != nil {
		m.Close()
		return nil, nil, fmt.Errorf("unlock pty: %w", err)
	}
	var n int32
	if err := ioctl(m.Fd(), tiocGPTN, uintptr(unsafe.Pointer(&n))); err != nil {
		m.Close()
		return nil, nil, fmt.Errorf("pty number: %w", err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	return m, s, nil
}

func ioctl(fd, req, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); e != 0 {
		return e
	}
	return nil
}
