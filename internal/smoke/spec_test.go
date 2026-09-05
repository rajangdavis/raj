//go:build smoke

package smoke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The chords, as the sequences internal/keys pins them to. Spelled here rather
// than imported so a scenario asserts on the wire the terminal actually sends:
// if the table changes and the sequence does not, the smoke test should notice
// before a user does.
const (
	cmdS  = "115;9u" // save
	cmdR  = "114;9u" // reload
	enter = "13u"
	esc   = "27u"
	right = "1;9C" // in a dialog, moves to the next answer
)

// --- saving -----------------------------------------------------------------

// The baseline: a save writes the buffer, and writes it atomically enough to
// leave nothing behind.
func TestSaveWritesTheBuffer(t *testing.T) {
	e := start(t, map[string]string{"notes.txt": "one\n"})
	e.open("notes.txt")
	e.typeText("X")
	e.press(cmdS)

	must(t, "on disk", e.onDisk("notes.txt"), "Xone\n")

	entries, err := os.ReadDir(e.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "notes.txt" {
			t.Errorf("save left %q behind", entry.Name())
		}
	}
}

// A script stays executable. The mode is copied from the file being replaced,
// and a fresh temp file is 0600, so this is the assertion that catches the
// atomic write forgetting to chmod.
func TestSavePreservesMode(t *testing.T) {
	e := start(t, map[string]string{"run.sh": "#!/bin/sh\n"})
	path := filepath.Join(e.root, "run.sh")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	e.open("run.sh")
	e.typeText("#")
	e.press(cmdS)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("mode = %o, want 755", got)
	}
}

// The property the whole stat guard exists for: another writer's work is not
// replaced without a question.
func TestSaveOverAChangedFileAsksFirst(t *testing.T) {
	e := start(t, map[string]string{"notes.txt": "mine\n"})
	e.open("notes.txt")
	e.typeText("X")
	e.rewrite("notes.txt", "theirs\n")
	e.press(cmdS)

	must(t, "on disk while the dialog is up", e.onDisk("notes.txt"), "theirs\n")

	e.press(enter) // Overwrite is the default answer on a dirty buffer
	must(t, "after overwrite", e.onDisk("notes.txt"), "Xmine\n")
}

func TestCancellingAConflictWritesNothing(t *testing.T) {
	e := start(t, map[string]string{"notes.txt": "mine\n"})
	e.open("notes.txt")
	e.typeText("X")
	e.rewrite("notes.txt", "theirs\n")
	e.press(cmdS, esc)

	must(t, "on disk", e.onDisk("notes.txt"), "theirs\n")
	must(t, "buffer", e.text("notes.txt"), "Xmine")
}

// --- reload -----------------------------------------------------------------

// Nothing to lose, so no question.
func TestReloadOnACleanBuffer(t *testing.T) {
	e := start(t, map[string]string{"notes.txt": "one\ntwo\n"})
	e.open("notes.txt")
	e.rewrite("notes.txt", "ONE\nTWO\n")
	e.press(cmdR)

	must(t, "buffer", e.text("notes.txt"), "ONE\nTWO")
	must(t, "on disk", e.onDisk("notes.txt"), "ONE\nTWO\n")
}

// The gesture that throws away the only copy of something asks once.
func TestReloadOverUnsavedChangesAsks(t *testing.T) {
	e := start(t, map[string]string{"notes.txt": "one\n"})
	e.open("notes.txt")
	e.typeText("X")
	e.rewrite("notes.txt", "theirs\n")
	e.press(cmdR)

	must(t, "buffer while the dialog is up", e.text("notes.txt"), "Xone")

	e.press(enter) // Discard
	must(t, "after discarding", e.text("notes.txt"), "theirs")
}

func TestCancellingAReloadKeepsTheEdits(t *testing.T) {
	e := start(t, map[string]string{"notes.txt": "one\n"})
	e.open("notes.txt")
	e.typeText("X")
	e.rewrite("notes.txt", "theirs\n")
	e.press(cmdR, esc)

	must(t, "buffer", e.text("notes.txt"), "Xone")
}

// --- encoding ---------------------------------------------------------------

// Open and save with no edit in between must not change a byte. This is the
// one that catches raj rewriting files it was only asked to look at.
func TestOpenAndSaveIsAByteRoundTrip(t *testing.T) {
	for name, content := range map[string]string{
		"lf.txt":       "a\nb\n",
		"crlf.txt":     "a\r\nb\r\n",
		"nofinal.txt":  "a\nb",
		"bom.txt":      "\ufeffa\nb\n",
		"bomcrlf.txt":  "\ufeffa\r\nb\r\n",
		"oneline.txt":  "single line",
		"unicode.txt":  "π → ∞\n",
		"trailing.txt": "a\n\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			e := start(t, map[string]string{name: content})
			e.open(name)
			// A keystroke and its undo, so the save is not skipped as a no-op
			// but the text is what it was.
			e.typeText("Z")
			e.press("122;9u") // cmd+z
			e.press(cmdS)

			if got := e.onDisk(name); got != content {
				t.Errorf("round trip changed the file\n old: %q\n new: %q", content, got)
			}
		})
	}
}

// A CRLF file edited in raj stays CRLF, on the new lines as well as the old.
func TestEditedCRLFFileStaysCRLF(t *testing.T) {
	e := start(t, map[string]string{"win.txt": "a\r\nb\r\n"})
	e.open("win.txt")
	e.typeText("X")
	e.press(cmdS)

	must(t, "on disk", e.onDisk("win.txt"), "Xa\r\nb\r\n")
}

// --- the binary itself ------------------------------------------------------

// The control address is how a harness finds the editor, and it has to be on
// stderr before the alternate screen swallows it. start() already depends on
// this; the assertion is here so the failure names the cause.
func TestControlAddressIsAnnounced(t *testing.T) {
	e := start(t, map[string]string{"notes.txt": "x\n"})
	if e.sock == "" {
		t.Fatal("no control address")
	}
	if got := e.ctl("buffers"); strings.Contains(got, "cannot reach") {
		t.Errorf("the announced address does not answer: %s", got)
	}
}

// raj as shipped opens a file, which is the one thing every other scenario
// assumes and none of them assert directly.
func TestEditorOpensAFile(t *testing.T) {
	e := start(t, map[string]string{"notes.txt": "hello\n"})
	e.open("notes.txt")
	must(t, "buffer", e.text("notes.txt"), "hello")
}
