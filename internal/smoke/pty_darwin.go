//go:build smoke && darwin

package smoke

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// openPTY returns a connected master/slave pair.
//
// macOS has no TIOCGPTN. It grants, unlocks and names the slave through three
// ioctls of its own, and TIOCPTYGNAME writes the path into a 128-byte buffer
// rather than returning a number.
const (
	tiocPTYGRANT = 0x20007454
	tiocPTYUNLK  = 0x20007452
	tiocPTYGNAME = 0x40807453
)

func openPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := ioctl(m.Fd(), tiocPTYGRANT, 0); err != nil {
		m.Close()
		return nil, nil, fmt.Errorf("grant pty: %w", err)
	}
	if err := ioctl(m.Fd(), tiocPTYUNLK, 0); err != nil {
		m.Close()
		return nil, nil, fmt.Errorf("unlock pty: %w", err)
	}
	var buf [128]byte
	if err := ioctl(m.Fd(), tiocPTYGNAME, uintptr(unsafe.Pointer(&buf[0]))); err != nil {
		m.Close()
		return nil, nil, fmt.Errorf("pty name: %w", err)
	}
	name := string(buf[:])
	if i := indexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	s, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	return m, s, nil
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func ioctl(fd, req, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); e != 0 {
		return e
	}
	return nil
}
