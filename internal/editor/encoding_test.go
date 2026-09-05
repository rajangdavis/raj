package editor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// The property that matters: opening a file and saving it without editing it
// must not change a single byte. Anything else means raj rewrites files it was
// only asked to look at, and the diff shows up in someone's commit.
func openSaveRoundTrip(t *testing.T, original string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SaveOver(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("round trip changed the file\n old: %q\n new: %q", original, got)
	}
}

func TestRoundTripPreservesBytes(t *testing.T) {
	cases := map[string]string{
		"lf":                "a\nb\nc\n",
		"crlf":              "a\r\nb\r\nc\r\n",
		"no final newline":  "a\nb",
		"crlf no final":     "a\r\nb",
		"bom lf":            bom + "a\nb\n",
		"bom crlf":          bom + "a\r\nb\r\n",
		"empty":             "",
		"only newline":      "\n",
		"only crlf":         "\r\n",
		"no newline at all": "single line",
		"utf8":              "π → ∞\n",
		"lone cr":           "a\rb\n", // an old-Mac ending raj leaves alone
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) { openSaveRoundTrip(t, content) })
	}
}

func TestBufferHoldsNormalisedText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte(bom+"a\r\nb\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Text(); got != "a\nb\n" {
		t.Fatalf("buffer text = %q, want %q", got, "a\nb\n")
	}
	if f.Lines() != 3 {
		t.Fatalf("lines = %d, want 3", f.Lines())
	}
	if !f.Enc.CRLF || !f.Enc.BOM {
		t.Fatalf("encoding = %+v, want CRLF and BOM", f.Enc)
	}
}

// An edited CRLF file keeps CRLF, including on the lines that were added.
func TestEditedCRLFFileStaysCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("a\r\nb\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	f.Insert(0, f.Len(), "c\n")
	if err := f.SaveOver(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a\r\nb\r\nc\r\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestMixedEndingsWarn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("a\r\nb\nc\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Enc.Mixed {
		t.Fatal("mixed endings not detected")
	}
	if w := f.EncodingWarning(); !strings.Contains(w, "mixed line endings") {
		t.Fatalf("warning = %q", w)
	}
}

// decode/encode is a round trip for any input that is not mixed — which is the
// exact condition EncodingWarning exists to report.
func FuzzEncodingRoundTrip(f *testing.F) {
	for _, s := range []string{"a\nb\n", "a\r\nb\r\n", bom + "x", "", "\r", "\n\r\n", "\r\r\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		if !utf8.ValidString(data) {
			t.Skip()
		}
		text, enc := decode(data)
		if enc.Mixed {
			t.Skip() // documented as lossy; EncodingWarning tells the user
		}
		if got := string(encode(text, enc)); got != data {
			t.Fatalf("round trip: %q -> %q", data, got)
		}
	})
}
