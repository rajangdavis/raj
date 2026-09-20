package editor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/piecetable"
)

func TestOpenSnapshotRendersTheRestoredSession(t *testing.T) {
	s := piecetable.NewSession(piecetable.NewDoc("hello world\nsecond line\n", 0))
	s.Insert(piecetable.Agent, 6, "XX")
	prop := s.LastGroup()
	s.MarkGroup(prop, piecetable.Proposed)
	snap, err := s.SnapshotState()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	// The file on disk is deliberately different: OpenSnapshot must not read it.
	if err := os.WriteFile(path, []byte("DIFFERENT\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := OpenSnapshot(path, snap, Encoding{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	want := s.Project(piecetable.AcceptedAndProposed).Text()
	if f.Text() != want {
		t.Errorf("text = %q, want %q", f.Text(), want)
	}
	if strings.Contains(f.Text(), "DIFFERENT") {
		t.Error("OpenSnapshot read the file on disk")
	}

	// The same text and line count as a disk-open of the composed equivalent.
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	disk, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if f.Text() != disk.Text() {
		t.Errorf("text = %q, disk-open = %q", f.Text(), disk.Text())
	}
	if f.Lines() != disk.Lines() {
		t.Errorf("lines = %d, disk-open = %d", f.Lines(), disk.Lines())
	}

	// The proposed set still marks its line.
	pane := NewPane(f)
	pane.UpdateDisplay(piecetable.AcceptedAndProposed)
	marks := pane.PendingMarks()
	if len(marks) != 1 || marks[0].Line != 0 {
		t.Fatalf("marks = %+v, want one on line 0", marks)
	}

	// A plain save is refused: the bytes are the daemon's, not this file's.
	if err := f.Save(); !errors.Is(err, ErrSnapshotReadOnly) {
		t.Errorf("Save = %v, want ErrSnapshotReadOnly", err)
	}
}
