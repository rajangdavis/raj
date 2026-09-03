package editor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// saved builds a pane over a real file on disk that has already been written,
// which is the state every dirty question is asked against.
func savedPane(t *testing.T, content string) *Pane {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.go")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPane(f)
	p.Resize(40, 10)
	return p
}

// The reported behaviour: undo does not rewind the version — it appends the
// reversing ops — so a buffer edited and put back used to stay marked dirty and
// ask to be saved on close.
func TestUndoBackToSavedIsClean(t *testing.T) {
	p := savedPane(t, "var x = 1\n")
	p.DocEnd(false)
	p.InsertText("noise")
	if !p.File.Dirty() {
		t.Fatal("a buffer with an edit in it must be dirty")
	}
	p.history(p.File.Undo(p.Author))
	if p.File.Text() != "var x = 1\n" {
		t.Fatalf("setup: undo left %q", p.File.Text())
	}
	if p.File.Dirty() {
		t.Error("a buffer undone back to the saved text must be clean")
	}
}

// Redoing puts the change back, so the buffer is dirty again.
func TestRedoAfterUndoIsDirty(t *testing.T) {
	p := savedPane(t, "var x = 1\n")
	p.DocEnd(false)
	p.InsertText("noise")
	p.history(p.File.Undo(p.Author))
	p.history(p.File.Redo(p.Author))
	if !p.File.Dirty() {
		t.Error("redo restores the change, so the buffer is dirty")
	}
}

// Dirtiness is a question about content, not about history: typing a character
// and deleting it again by hand leaves the file as it was on disk.
func TestManualRevertIsClean(t *testing.T) {
	p := savedPane(t, "var x = 1\n")
	p.DocEnd(false)
	p.InsertText("z")
	p.DeleteBackward()
	if p.File.Dirty() {
		t.Error("a buffer typed into and typed back out of must be clean")
	}
}

// Same length, different bytes. A length check alone would call this clean.
func TestSameLengthDifferentTextIsDirty(t *testing.T) {
	p := savedPane(t, "var x = 1\n")
	p.Cursors.Set(4, 4)
	p.DeleteBackward()
	p.InsertText("y")
	if p.File.Text() == "var x = 1\n" {
		t.Fatalf("setup: the text should differ")
	}
	if !p.File.Dirty() {
		t.Error("text of the same length but different content is dirty")
	}
}

// Saving records what went to disk, so the next comparison is against that
// rather than against what was opened.
func TestSaveResetsTheComparison(t *testing.T) {
	p := savedPane(t, "var x = 1\n")
	p.DocEnd(false)
	p.InsertText("var y = 2\n")
	if err := p.File.Save(); err != nil {
		t.Fatal(err)
	}
	if p.File.Dirty() {
		t.Fatal("a just-saved buffer is clean")
	}
	p.history(p.File.Undo(p.Author))
	if !p.File.Dirty() {
		t.Error("undoing past the last save is a change, so the buffer is dirty")
	}
}

// An unnamed buffer starts clean and empty, and typing into it and deleting it
// all again returns it to that state.
func TestEmptyBufferRoundTrip(t *testing.T) {
	p := newTestPane("")
	if p.File.Dirty() {
		t.Fatal("a new empty buffer is not dirty")
	}
	p.InsertText("hello")
	if !p.File.Dirty() {
		t.Fatal("typing makes it dirty")
	}
	for range "hello" {
		p.DeleteBackward()
	}
	if p.File.Dirty() {
		t.Error("emptying it again returns it to the state it was created in")
	}
}

// Past MaxCleanCheck the comparison would cost a visible stall, so an edited
// buffer stays dirty rather than being re-read every version.
func TestHugeBufferSkipsTheContentCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates past MaxCleanCheck")
	}
	p := newTestPane(strings.Repeat("x", MaxCleanCheck+1))
	p.DocEnd(false)
	p.InsertText("z")
	p.DeleteBackward()
	if !p.File.Dirty() {
		t.Error("past the size guard the buffer stays dirty; the check is skipped")
	}
}

// Dirty is asked once per open tab per frame, so the content comparison has to
// be memoised per version rather than re-read on every call.
func BenchmarkDirtyRepeated(b *testing.B) {
	p := newTestPane(strings.Repeat("var x = 1\n", 20000))
	p.DocEnd(false)
	p.InsertText("z")
	p.DeleteBackward() // same length as the opened content: the expensive case
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if p.File.Dirty() {
			b.Fatal("expected clean")
		}
	}
}
