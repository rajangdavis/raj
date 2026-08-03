package editor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsBinary(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"plain text", "package main\n\nfunc main() {}\n", false},
		{"utf8 text", "héllo 日本語 🎉\n", false},
		{"empty", "", false},
		{"nul byte", "text\x00more", true},
		{"elf header", "\x7fELF\x02\x01\x01\x00", true},
		{"invalid utf8", "\xff\xfe\xfd\xfc", true},
		{"long text", strings.Repeat("a line of source\n", 2000), false},
		// A rune split by the sniff window is not evidence of binary.
		{"split rune at boundary", strings.Repeat("a", sniffLen-1) + "日本語text", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsBinary(c.content); got != c.want {
				t.Errorf("IsBinary = %v, want %v", got, c.want)
			}
		})
	}
}

func TestOpenRefusesBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "a.out")
	os.WriteFile(bin, []byte("\x7fELF\x02\x00\x00\x00\x1b[2J"), 0o644)
	if _, err := Open(bin, 2); err != ErrBinary {
		t.Errorf("err = %v, want ErrBinary", err)
	}
}

func TestOpenRefusesDirectory(t *testing.T) {
	if _, err := Open(t.TempDir(), 2); err == nil {
		t.Error("opening a directory should fail")
	}
}

// A file that does not exist yet opens empty, so naming a new file works.
func TestOpenMissingIsEmpty(t *testing.T) {
	f, err := Open(filepath.Join(t.TempDir(), "new.go"), 2)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if f.Len() != 0 {
		t.Errorf("len = %d, want 0", f.Len())
	}
}
