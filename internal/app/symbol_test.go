package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goSrc = `package main

import "fmt"

type Cat struct {
	Name string
}

const Loud = 11

func (c *Cat) Meow() {
	fmt.Println("meow")
}

func main() {
	c := &Cat{}
	c.Meow()
}
`

// cmd+shift+o was a bound chord with nothing behind it, so it was taken from
// the terminal for nothing.
func TestGotoSymbolOpensTheOverlay(t *testing.T) {
	h := newHarness(t, goSrc)
	h.press("shift+super+o")

	if !h.Picker.Open || h.Focused() != FocusPicker {
		t.Fatal("cmd+shift+o did not open the overlay")
	}
	if h.Picker.Results() != 4 {
		t.Errorf("listed %d symbols, want 4", h.Picker.Results())
	}
	// File order with no query: the list is read alongside the file.
	if got := h.Picker.Top(); !strings.HasPrefix(got, "Cat") {
		t.Errorf("first row %q, want Cat", got)
	}
}

func TestGotoSymbolJumps(t *testing.T) {
	h := newHarness(t, goSrc)
	h.press("shift+super+o")
	h.typeText("meow")
	h.press("enter")

	if h.Picker.Open {
		t.Error("choosing a symbol left the overlay open")
	}
	if h.Focused() != FocusEditor {
		t.Error("focus did not return to the editor")
	}
	if got := cursorLine(h); got != 11 {
		t.Errorf("cursor on line %d, want 11 (func (c *Cat) Meow)", got)
	}
}

// Jumping within the file must not open a second tab for it.
func TestGotoSymbolStaysInOneTab(t *testing.T) {
	h := newHarness(t, goSrc)
	before := len(h.Tabs.All())
	h.press("shift+super+o")
	h.typeText("main")
	h.press("enter")

	if got := len(h.Tabs.All()); got != before {
		t.Errorf("tab count went from %d to %d", before, got)
	}
	if got := cursorLine(h); got != 15 {
		t.Errorf("cursor on line %d, want 15", got)
	}
}

// Escaping leaves the cursor alone, the same as cancelling go-to-line.
func TestGotoSymbolCancelLeavesTheCursor(t *testing.T) {
	h := newHarness(t, goSrc)
	h.press("ctrl+g")
	h.typeText("6")
	h.press("enter")
	h.press("shift+super+o")
	h.press("esc")

	if h.Picker.Open {
		t.Error("escape left the overlay open")
	}
	if got := cursorLine(h); got != 6 {
		t.Errorf("cursor moved to %d, want it left on 6", got)
	}
}

// A file type with no rules says so, rather than reporting an empty list that
// looks like a file with no declarations in it.
func TestGotoSymbolOnAnUnsupportedFile(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	notes := filepath.Join(h.root, "notes.txt")
	if err := os.WriteFile(notes, []byte("func not_go() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(notes)
	h.drain()
	h.press("shift+super+o")

	if h.Picker.Open {
		t.Error("opened an overlay with nothing to list")
	}
	if !strings.Contains(h.Status(), "no symbols") {
		t.Errorf("status = %q, want it to mention symbols", h.Status())
	}
}

// The two modes share one overlay, so opening either must not leave the other's
// rows behind.
func TestModesDoNotLeakIntoEachOther(t *testing.T) {
	h := newHarness(t, goSrc)
	h.press("shift+super+o")
	symbolRows := h.Picker.Results()
	h.press("esc")
	h.press("super+p")
	fileRows := h.Picker.Results()
	if fileRows == symbolRows {
		t.Fatalf("both modes listed %d rows; the setup cannot tell them apart", fileRows)
	}
	if got := h.Picker.Mode(); got != 0 {
		t.Errorf("cmd+p left the overlay in mode %v, want Files", got)
	}
	h.press("esc")
	h.press("shift+super+o")
	if got := h.Picker.Results(); got != symbolRows {
		t.Errorf("reopening symbols listed %d rows, want %d", got, symbolRows)
	}
}
