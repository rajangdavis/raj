package editor

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/piecetable"
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

// Open and save with no edit is byte-identical for every encoding raj detects,
// not just UTF-8. The whole file must survive the round trip — BOM, code units
// and high bytes alike — or the first save silently rewrites it.
func TestEncodingRoundTripPreservesBytes(t *testing.T) {
	cases := map[string]string{
		"utf16le ascii":  "\xff\xfe" + "h\x00i\x00\n\x00",
		"utf16be ascii":  "\xfe\xff" + "\x00h\x00i\x00\n",
		"utf16le crlf":   "\xff\xfe" + "a\x00\r\x00\n\x00",
		"utf16le astral": "\xff\xfe" + "\x3d\xd8\x00\xde\n\x00",
		"cp1252 quotes":  "smart \x93quotes\x94\n",
		"latin1 accents": "caf\xe9 na\xefve\n",
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

// A BOM is a decision, not a side effect. A UTF-16 file used to be refused as
// binary because of the NULs that are half its bytes; it now decodes to the
// text it holds, under the byte order its mark names.
func TestOpenDecodesUTF16(t *testing.T) {
	cases := map[string]struct {
		data string
		kind Kind
		want string
	}{
		"le ascii": {
			data: "\xff\xfe" + "h\x00i\x00\n\x00",
			kind: UTF16LE,
			want: "hi\n",
		},
		"be ascii": {
			data: "\xfe\xff" + "\x00h\x00i\x00\n",
			kind: UTF16BE,
			want: "hi\n",
		},
		// A surrogate pair is two code units and one character. The buffer
		// holds the character, so the pair is a character and not two bytes of
		// noise to be replaced.
		"le surrogate pair": {
			data: "\xff\xfe" + "\x3d\xd8\x00\xde",
			kind: UTF16LE,
			want: "\U0001F600",
		},
		"be crlf": {
			data: "\xfe\xff" + "\x00a\x00\r\x00\n\x00b",
			kind: UTF16BE,
			want: "a\nb",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(path, []byte(c.data), 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := Open(path, 4)
			if err != nil {
				t.Fatal(err)
			}
			if got := f.Text(); got != c.want {
				t.Fatalf("text = %q, want %q", got, c.want)
			}
			if f.Enc.Kind != c.kind {
				t.Fatalf("kind = %v, want %v", f.Enc.Kind, c.kind)
			}
			if !f.Enc.BOM {
				t.Fatal("UTF-16 file lost its BOM flag")
			}
		})
	}
}

// An edit in a UTF-16 file re-encodes as UTF-16, not UTF-8: the added line is
// written in the file's code units and the BOM stays. The result is checked by
// decoding it back, so "well-formed" is a property and not a byte guess.
func TestEditedUTF16FileStaysUTF16(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("\xff\xfe"+"a\x00\n\x00"), 0o644); err != nil {
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
	want := "\xff\xfe" + "a\x00\n\x00c\x00\n\x00"
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("content = %q, want %q", got, want)
	}
	text, enc, err := decode(got)
	if err != nil {
		t.Fatalf("saved bytes do not decode: %v", err)
	}
	if text != "a\nc\n" {
		t.Fatalf("decoded text = %q, want %q", text, "a\nc\n")
	}
	if enc.Kind != UTF16LE {
		t.Fatalf("decoded kind = %v, want UTF16LE", enc.Kind)
	}
}

// 0x93 and 0x94 are the curly quotes CP1252 gives those bytes; a file holding
// them cannot be UTF-8, so the old sniffer called it binary. It is text, and
// the Windows mapping is what the bytes mean.
func TestOpenDecodesWindows1252(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("smart \x93quotes\x94\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Text(); got != "smart \u201cquotes\u201d\n" {
		t.Fatalf("text = %q", got)
	}
	if f.Enc.Kind != Windows1252 {
		t.Fatalf("kind = %v, want Windows1252", f.Enc.Kind)
	}
}

// A high byte with no CP1252 punctuation in the file is read as Latin-1. The
// two mappings agree everywhere except 0x80-0x9F, so a file with none of those
// is correctly named either way and still has to round-trip.
func TestOpenDecodesLatin1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("caf\xe9 na\xefve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Text(); got != "caf\u00e9 na\u00efve\n" {
		t.Fatalf("text = %q", got)
	}
	if f.Enc.Kind != Latin1 {
		t.Fatalf("kind = %v, want Latin1", f.Enc.Kind)
	}
}

// A UTF-32 file starts with the UTF-16 little-endian BOM plus a NUL. raj can
// name it, cannot reproduce it, and refuses it by name rather than decoding it
// as UTF-16 or calling it binary.
func TestOpenRefusesUnsupportedEncoding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	data := "\xff\xfe\x00\x00" + "h\x00\x00\x00"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path, 4)
	if !errors.Is(err, ErrUnsupportedEncoding) {
		t.Fatalf("Open() = %v, want ErrUnsupportedEncoding", err)
	}
	if !strings.Contains(err.Error(), "UTF-32") {
		t.Fatalf("message = %q, want it to name UTF-32", err)
	}
}

// UTF-16 without a BOM is a guess raj will not make: the bytes could be text or
// could be binary. It is refused by name rather than decoded speculatively.
func TestOpenRefusesUnmarkedUTF16(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	data := "h\x00i\x00\n\x00"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path, 4)
	if !errors.Is(err, ErrUnsupportedEncoding) {
		t.Fatalf("Open() = %v, want ErrUnsupportedEncoding", err)
	}
	if !strings.Contains(err.Error(), "UTF-16") {
		t.Fatalf("message = %q, want it to name UTF-16", err)
	}
}

// The wider acceptance must not swallow binary. A NUL outside the unmarked
// UTF-16 shape, and the control bytes an executable carries, are still refused.
func TestOpenStillRefusesBinary(t *testing.T) {
	cases := map[string]string{
		"elf":              "\x7fELF\x02\x01\x01\x00",
		"nul in text":      "text\x00more",
		"control and high": "\x01\xff",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.txt")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path, 4); !errors.Is(err, ErrBinary) {
				t.Fatalf("Open() = %v, want ErrBinary", err)
			}
		})
	}
}

// A character the file's encoding has no byte for refuses the save. The
// alternatives — writing UTF-8 under a Latin-1 name, or substituting a
// placeholder — are files that no longer hold what is on screen, and neither
// may happen without the user asking.
func TestSaveRefusesUnencodableText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	original := "caf\xe9\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	f.Insert(0, f.Len(), "\u2192") // an arrow, in no single-byte western encoding
	if err := f.SaveOver(); !errors.Is(err, ErrUnencodableText) {
		t.Fatalf("SaveOver() = %v, want ErrUnencodableText", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("a refused save changed the file: %q", got)
	}
}

// openSingleByte opens a file whose bytes are a single-byte western encoding,
// so a rune that encoding has no byte for makes encode refuse. It is the
// fixture TestSaveRefusesUnencodableText builds inline; these tests ask a
// second question of the same state, so the setup is shared.
func openSingleByte(t *testing.T) *File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("caf\xe9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A save that fails to encode must be a genuine no-op: the pending set the
// save would have approved is still proposed, and neither the buffer nor the
// file moved. Without the rollback in SaveOver the accept has already happened
// by the time encode refuses, so Pending is empty and the state reads accepted
// — that is the defect this pins. Sibling: TestSaveRefusesUnencodableText,
// which pins the bytes for an already-accepted edit and cannot see the
// approval side effect.
func TestSaveFailedEncodeLeavesPendingProposed(t *testing.T) {
	f := openSingleByte(t)
	id := proposeAt(t, f, 0, 0, "\u2192") // an arrow only the proposed text carries
	if got := len(f.Session().Pending()); got != 1 {
		t.Fatalf("pending = %d, want 1", got)
	}
	before := f.Text()
	if err := f.SaveOver(); !errors.Is(err, ErrUnencodableText) {
		t.Fatalf("SaveOver() = %v, want ErrUnencodableText", err)
	}
	if got := len(f.Session().Pending()); got != 1 {
		t.Errorf("pending after a refused save = %d, want the proposal still pending", got)
	}
	if got := f.Session().GroupState(id); got != piecetable.Proposed {
		t.Errorf("state after a refused save = %v, want Proposed", got)
	}
	if got := f.Text(); got != before {
		t.Errorf("a refused save changed the buffer: %q, want %q", got, before)
	}
	got, err := os.ReadFile(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "caf\xe9\n" {
		t.Errorf("a refused save changed the file: %q", got)
	}
}

// The rollback must not swallow or replace the refusal: the wrapped
// ErrUnencodableText still reaches the caller when the unencodable character
// arrived in a pending set. Sibling: TestSaveRefusesUnencodableText.
func TestSaveFailedEncodeStillReturnsErrUnencodableText(t *testing.T) {
	f := openSingleByte(t)
	proposeAt(t, f, 0, 0, "\u2192")
	if err := f.SaveOver(); !errors.Is(err, ErrUnencodableText) {
		t.Fatalf("SaveOver() = %v, want ErrUnencodableText", err)
	}
}

// The successful path is unchanged: a save still accepts the pending set and
// writes the accepted composition, and the buffer reads clean afterwards.
// Sibling: TestSaveAcceptsPendingProposals (layered_test.go), here on the
// single-byte fixture so the encoding is known to round trip.
func TestSaveAcceptStillAcceptsPendingAndWrites(t *testing.T) {
	f := openSingleByte(t)
	id := proposeAt(t, f, 0, 0, "x") // encodable in the file's encoding
	if err := f.SaveOver(); err != nil {
		t.Fatalf("SaveOver() = %v, want a successful save", err)
	}
	if got := len(f.Session().Pending()); got != 0 {
		t.Errorf("pending after save = %d, want none", got)
	}
	if got := f.Session().GroupState(id); got != piecetable.Accepted {
		t.Errorf("state after save = %v, want Accepted", got)
	}
	got, err := os.ReadFile(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "xcaf\xe9\n" {
		t.Errorf("on disk = %q, want the accepted proposal written", got)
	}
	if f.Dirty() {
		t.Error("a buffer saved with its proposal accepted is clean")
	}
}

// decode/encode is a round trip for any input raj accepts that is not mixed —
// which is the exact condition EncodingWarning exists to report. The encodings
// that cannot make that promise — binary, unsupported, malformed — are refused
// by decode before this runs, and are the skip.
func FuzzEncodingRoundTrip(f *testing.F) {
	for _, s := range []string{
		"a\nb\n", "a\r\nb\r\n", bom + "x", "", "\r", "\n\r\n", "\r\r\n",
		"caf\xe9\n", "smart \x93quotes\x94\n",
		"\xff\xfe" + "h\x00i\x00", "\xfe\xff" + "\x00h\x00i",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		text, enc, err := decode(data)
		if err != nil {
			t.Skip() // binary, unsupported or malformed; refused by decision
		}
		if enc.Mixed {
			t.Skip() // documented as lossy; EncodingWarning tells the user
		}
		got, err := encode(text, enc)
		if err != nil {
			t.Fatalf("encode failed for a file that decoded: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("round trip: %q -> %q", data, got)
		}
	})
}

// The acceptance a save performs must be rolled back whole when the write it
// stands for cannot happen. This is the SaveOver rollback the TODO names: every
// pending set is accepted before encode can refuse, so a refusal has to put all
// of them back, move no version and no decision generation, and leave the file
// on disk and both projections exactly as they were. A test on a single set
// (TestSaveFailedEncodeLeavesPendingProposed) cannot see a rollback that
// restores the first set and drops the rest, so this one holds two pending sets
// with the unencodable run in the older of them.
func TestSaveFailedEncodeRollsBackEveryAcceptance(t *testing.T) {
	f := openSingleByte(t)
	// The arrow is unencodable in this file's single-byte encoding, so the
	// write is guaranteed to refuse after both sets have been accepted.
	older := proposeAt(t, f, 0, 0, "\u2192")
	newer := proposeAt(t, f, 4, 4, "!")

	if len(f.Session().Pending()) != 2 {
		t.Fatalf("setup: pending = %d, want 2", len(f.Session().Pending()))
	}
	beforeText := f.Text()
	beforeEdit := f.Session().Project(piecetable.AcceptedAndProposed).Text()
	beforeAgreed := f.Session().Project(piecetable.AcceptedOnly).Text()
	beforeVersion := f.Session().Version()
	beforeOps := len(f.Session().Journal())
	beforeGen := f.DecisionGeneration()

	if err := f.SaveOver(); !errors.Is(err, ErrUnencodableText) {
		t.Fatalf("SaveOver() = %v, want ErrUnencodableText", err)
	}

	if got := f.Session().GroupState(older); got != piecetable.Proposed {
		t.Errorf("older set state = %v, want Proposed", got)
	}
	if got := f.Session().GroupState(newer); got != piecetable.Proposed {
		t.Errorf("newer set state = %v, want Proposed", got)
	}
	if got := len(f.Session().Pending()); got != 2 {
		t.Errorf("pending after a refused save = %d, want both sets still pending", got)
	}
	if got := f.Session().Version(); got != beforeVersion {
		t.Errorf("version moved by a refused save: %d -> %d", beforeVersion, got)
	}
	if got := len(f.Session().Journal()); got != beforeOps {
		t.Errorf("journal grew on a refused save: %d -> %d ops", beforeOps, got)
	}
	if got := f.DecisionGeneration(); got != beforeGen {
		t.Errorf("decision generation moved by a refused save: %d -> %d", beforeGen, got)
	}
	if got := f.Text(); got != beforeText {
		t.Errorf("a refused save changed the buffer: %q, want %q", got, beforeText)
	}
	if got := f.Session().Project(piecetable.AcceptedAndProposed).Text(); got != beforeEdit {
		t.Errorf("edit projection changed: %q, want %q", got, beforeEdit)
	}
	if got := f.Session().Project(piecetable.AcceptedOnly).Text(); got != beforeAgreed {
		t.Errorf("agreed projection changed: %q, want %q", got, beforeAgreed)
	}
	got, err := os.ReadFile(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "caf\xe9\n" {
		t.Errorf("a refused save changed the file: %q", got)
	}
}
