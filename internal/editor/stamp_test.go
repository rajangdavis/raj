package editor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// touch rewrites a file with a modification time far enough in the future that
// the check cannot depend on filesystem timestamp granularity.
func touch(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

func TestSaveRefusesWhenDiskChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	f.Insert(0, 0, "mine ")

	touch(t, path, "written by something else\n")

	if err := f.Save(); !errors.Is(err, ErrDiskChanged) {
		t.Fatalf("Save() = %v, want ErrDiskChanged", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "written by something else\n" {
		t.Fatalf("refused save still wrote: %q", got)
	}
	if !f.Dirty() {
		t.Fatal("buffer marked clean by a save that did not happen")
	}
}

func TestSaveOverIgnoresTheConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	f.Insert(0, 0, "mine ")
	touch(t, path, "theirs\n")

	if err := f.SaveOver(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "mine original\n" {
		t.Fatalf("content = %q", got)
	}
	// And the stamp is refreshed, so the next save is not blocked by the
	// write this one just made.
	if err := f.Save(); err != nil {
		t.Fatalf("second save = %v, want nil", err)
	}
}

func TestSaveTwiceInARowIsNotAConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	f := NewFile(path, "a", 4)
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	f.Insert(0, 1, "b")
	if err := f.Save(); err != nil {
		t.Fatalf("raj's own write tripped the guard: %v", err)
	}
}

// A save-as target the buffer has never been stamped against must not be
// judged by the previous file's mtime.
func TestStampIsPerPath(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.txt")
	second := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(first, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(first, 4)
	if err != nil {
		t.Fatal(err)
	}
	touch(t, first, "changed")
	f.SetPath(second)
	if f.DiskChanged() {
		t.Fatal("a different path reported as changed")
	}
	if err := f.Save(); err != nil {
		t.Fatalf("save-as refused: %v", err)
	}
}

func TestUnnamedBufferNeverConflicts(t *testing.T) {
	f := NewFile("", "scratch", 4)
	if f.DiskChanged() {
		t.Fatal("an unnamed buffer cannot conflict with anything")
	}
}
