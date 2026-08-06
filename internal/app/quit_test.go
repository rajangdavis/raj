package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/prompt"
)

// cmd+w guards one tab; quit has every unsaved tab behind it. Leaving it
// unguarded made the guard a property of the chord rather than of the buffer.

func TestQuitWithNothingDirtyExitsImmediately(t *testing.T) {
	h := newHarness(t, "untouched")
	h.press("ctrl+c")

	if h.Prompt.Open {
		t.Fatal("quitting a clean session asked a question")
	}
	if !h.quit {
		t.Error("did not quit")
	}
}

func TestQuitWithADirtyTabAsks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		answer   []string
		wantQuit bool
		wantDisk string
	}{
		{"save", []string{"enter"}, true, "editedbase"},
		{"discard", []string{"right", "enter"}, true, "base"},
		{"cancel", []string{"right", "right", "enter"}, false, "base"},
		{"escape", []string{"esc"}, false, "base"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "base")
			path := h.Pane().File.Path
			h.typeText("edited")
			h.press("ctrl+c")

			if !h.Prompt.Open {
				t.Fatal("quitting with unsaved work did not ask")
			}
			if got := h.Prompt.Selected(); got != prompt.Save {
				t.Errorf("default answer = %q, want %q", got, prompt.Save)
			}
			h.press(tc.answer...)

			if h.quit != tc.wantQuit {
				t.Errorf("quit = %v, want %v", h.quit, tc.wantQuit)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.wantDisk {
				t.Errorf("on disk = %q, want %q", data, tc.wantDisk)
			}
		})
	}
}

// One dirty file is named; several are counted, because a list of names does
// not fit and a bare count withholds the only useful detail when there is one.
func TestQuitMessageNamesOneAndCountsMany(t *testing.T) {
	h := newHarness(t, "base")
	h.typeText("x")
	h.press("ctrl+c")
	if got := h.host.Text(); !strings.Contains(got, "test.go") {
		t.Errorf("single dirty file not named on screen:\n%s", got)
	}
	h.press("esc")

	h.press("super+n")
	h.typeText("scratch")
	h.press("ctrl+c")
	if got := h.host.Text(); !strings.Contains(got, "2 files") {
		t.Errorf("two dirty files not counted on screen:\n%s", got)
	}
}

// Save must reach every dirty tab, not only the focused one.
func TestQuitSavesAllDirtyTabs(t *testing.T) {
	h := newHarness(t, "one")
	first := h.Pane().File.Path
	h.typeText("A")

	second := filepath.Join(h.root, "second.go")
	if err := os.WriteFile(second, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(second)
	h.drain()
	h.typeText("B")

	h.press("ctrl+c")
	h.press("enter") // Save

	if !h.quit {
		t.Fatal("did not quit after saving")
	}
	for path, want := range map[string]string{first: "Aone", second: "Btwo"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Errorf("%s on disk = %q, want %q", filepath.Base(path), data, want)
		}
	}
}

// An unnamed buffer stops for a path on the way out, and cancelling that path
// cancels the quit — exiting anyway would discard the work the answer asked to
// keep.
func TestQuitChainsIntoSaveAsAndCancellingAbortsTheQuit(t *testing.T) {
	h := newHarness(t, "")
	h.press("super+n")
	h.typeText("precious")
	h.press("ctrl+c")
	h.press("enter") // Save

	if !h.Prompt.Open || h.Prompt.Title() != "Save as" {
		t.Fatalf("did not ask for a path; dialog = %q", h.Prompt.Title())
	}
	h.press("esc")

	if h.quit {
		t.Fatal("quit despite the path being cancelled")
	}
	if got := h.Pane().File.Text(); got != "precious" {
		t.Errorf("buffer = %q, want precious", got)
	}

	// Answering it properly does quit, and writes the file.
	h.press("ctrl+c")
	h.press("enter")
	h.typeText("kept.txt")
	h.press("enter")

	if !h.quit {
		t.Fatal("did not quit after a successful save")
	}
	data, err := os.ReadFile(filepath.Join(h.root, "kept.txt"))
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if string(data) != "precious" {
		t.Errorf("on disk = %q", data)
	}
}

// ctrl+c is what people press when they want out now. A dialog that answers it
// by asking the same question again is a wedge, so the second press forces it.
func TestSecondQuitForcesTheExit(t *testing.T) {
	h := newHarness(t, "base")
	h.typeText("edited")
	h.press("ctrl+c")
	if !h.Prompt.Open {
		t.Fatal("first quit did not ask")
	}
	h.press("ctrl+c")

	if !h.quit {
		t.Error("a second quit did not force the exit")
	}
}

// Cancelling must leave the session able to ask again, rather than latching.
func TestQuitCanBeAskedAgainAfterCancelling(t *testing.T) {
	h := newHarness(t, "base")
	h.typeText("edited")
	h.press("ctrl+c")
	h.press("esc")

	h.press("ctrl+c")
	if !h.Prompt.Open {
		t.Error("the second quit did not ask; the guard latched off")
	}
	if h.quit {
		t.Error("quit without an answer")
	}
}
