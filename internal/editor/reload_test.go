package editor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T, content string) (*File, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	return f, path
}

func TestReloadTakesWhatIsOnDisk(t *testing.T) {
	f, path := openTemp(t, "one\ntwo\n")
	f.Insert(0, 0, "mine ")
	touch(t, path, "theirs\nand more\n")

	if err := f.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := f.Text(); got != "theirs\nand more\n" {
		t.Fatalf("text = %q", got)
	}
	if f.Dirty() {
		t.Fatal("a freshly reloaded buffer is not dirty")
	}
	// And the guard is satisfied again, so a save right after a reload works.
	if err := f.Save(); err != nil {
		t.Fatalf("save after reload = %v", err)
	}
}

// The undo journal does not cross a reload: the offsets in it belong to a
// document that no longer exists.
func TestReloadEndsTheUndoHistory(t *testing.T) {
	f, path := openTemp(t, "one\n")
	f.Insert(0, 0, "X")
	touch(t, path, "replaced\n")
	if err := f.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Undo(0); ok {
		t.Fatal("undo reached across a reload")
	}
	if got := f.Text(); got != "replaced\n" {
		t.Fatalf("text = %q after an undo that should not have happened", got)
	}
}

// A file rewritten by a Windows tool comes back CRLF, and the buffer has to
// notice — otherwise the next save writes LF into a file that was CRLF when it
// was last read.
func TestReloadPicksUpTheNewEncoding(t *testing.T) {
	f, path := openTemp(t, "a\nb\n")
	if f.Enc.CRLF {
		t.Fatal("setup: opened LF file reports CRLF")
	}
	touch(t, path, "a\r\nb\r\n")
	if err := f.Reload(); err != nil {
		t.Fatal(err)
	}
	if !f.Enc.CRLF {
		t.Fatal("reload did not pick up CRLF")
	}
	if got := f.Text(); got != "a\nb\n" {
		t.Fatalf("buffer text = %q, want the normalised form", got)
	}
	if err := f.SaveOver(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a\r\nb\r\n" {
		t.Fatalf("saved %q, want CRLF preserved", got)
	}
}

// A build can drop a binary onto a path that held source. Reloading that into a
// text buffer would put bytes on screen that cannot be edited or written back.
func TestReloadRefusesABinaryReplacement(t *testing.T) {
	f, path := openTemp(t, "source\n")
	touch(t, path, "\x00\x01\x02binary")
	if err := f.Reload(); !errors.Is(err, ErrBinary) {
		t.Fatalf("Reload() = %v, want ErrBinary", err)
	}
	if got := f.Text(); got != "source\n" {
		t.Fatalf("a refused reload changed the buffer: %q", got)
	}
}

func TestReloadOfAnUnnamedBufferIsRefused(t *testing.T) {
	f := NewFile("", "scratch", 4)
	if err := f.Reload(); err == nil {
		t.Fatal("an unnamed buffer has nothing to reload from")
	}
}

// The caret keeps its line and column, because a byte offset into the old text
// means nothing in the new one.
func TestPaneReloadKeepsTheCaretLine(t *testing.T) {
	f, path := openTemp(t, "one\ntwo\nthree\n")
	p := NewPane(f)
	p.Resize(40, 10)
	off := f.OffsetAt(2, 3) // line "three", column 3
	p.Cursors.Set(off, off)

	touch(t, path, "ONE\nTWO\nTHREE\nfour\n")
	if err := p.Reload(); err != nil {
		t.Fatal(err)
	}
	line, col := f.LineCol(p.Cursors.Primary().Head)
	if line != 2 || col != 3 {
		t.Fatalf("caret at %d:%d, want 2:3", line, col)
	}
}

// A file that shrank puts the caret at the end rather than out of bounds.
func TestPaneReloadClampsToAShorterFile(t *testing.T) {
	f, path := openTemp(t, "one\ntwo\nthree\nfour\n")
	p := NewPane(f)
	p.Resize(40, 10)
	off := f.OffsetAt(3, 2)
	p.Cursors.Set(off, off)

	touch(t, path, "one\n")
	if err := p.Reload(); err != nil {
		t.Fatal(err)
	}
	head := p.Cursors.Primary().Head
	if head > f.Len() {
		t.Fatalf("caret at %d, past the end of a %d-byte file", head, f.Len())
	}
}

// Extra cursors and selections are claims about places in a document that no
// longer exists.
func TestPaneReloadCollapsesToOneCaret(t *testing.T) {
	f, path := openTemp(t, "one\ntwo\nthree\n")
	p := NewPane(f)
	p.Resize(40, 10)
	p.Cursors.Set(0, 3) // a selection
	p.Cursors.Add(f.OffsetAt(1, 0), f.OffsetAt(1, 2))

	touch(t, path, "different\n")
	if err := p.Reload(); err != nil {
		t.Fatal(err)
	}
	if n := p.Cursors.Count(); n != 1 {
		t.Fatalf("cursors = %d, want 1", n)
	}
	if p.Cursors.Primary().HasSelection() {
		t.Fatal("a selection survived the reload")
	}
}
