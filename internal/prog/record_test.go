package prog

import (
	"bytes"
	"testing"
)

// A stand-in for the shape a buffers reply has: a list of elements, each a few
// fields in a fixed order, all in one op's payload.
type buffer struct {
	path    string
	version int
	dirty   bool
}

func writeBuffers(bufs []buffer) Op {
	var w Writer
	for _, b := range bufs {
		w.Str(b.path).Num(b.version).Bool(b.dirty)
	}
	return Op{Code: OpBuffers, Payload: w.Done()}
}

func readBuffers(payload []byte) ([]buffer, bool) {
	r := NewReader(payload)
	var out []buffer
	for r.More() {
		out = append(out, buffer{path: r.Str(), version: r.Num(), dirty: r.Bool()})
	}
	return out, !r.Bad
}

func TestRecordListRoundTrip(t *testing.T) {
	want := []buffer{
		{"/w/a.go", 41, true},
		{"/w/b.go", 0, false},
		{"/w/caf\xe9.txt", 70000, true}, // a path that is not valid UTF-8
	}
	got, ok := readBuffers(writeBuffers(want).Payload)
	if !ok {
		t.Fatal("reader reported a malformed payload")
	}
	if len(got) != len(want) {
		t.Fatalf("read %d buffers, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("buffer %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The element count is the payload, not a number in front of it, so an empty
// list is an empty payload rather than a special case.
func TestEmptyListIsAnEmptyPayload(t *testing.T) {
	op := writeBuffers(nil)
	if len(op.Payload) != 0 {
		t.Errorf("empty list encoded to %d bytes", len(op.Payload))
	}
	got, ok := readBuffers(op.Payload)
	if !ok || len(got) != 0 {
		t.Errorf("read %d buffers from an empty payload (ok=%v)", len(got), ok)
	}
}

// Records ride inside a program like any other op, and the whole thing stays
// free of zero bytes so it still fits in argv.
func TestRecordsInAProgramHaveNoZeroBytes(t *testing.T) {
	bufs := []buffer{{"/w/a.go", 0, false}, {"/w/" + string(bytes.Repeat([]byte("x"), 300)), 65536, true}}
	b := Encode([]Op{writeBuffers(bufs), {Code: OpBuffers}})
	if HasNul(b) {
		t.Fatal("a record put a zero byte in a program")
	}
	ops, err := Decode(b, map[byte]bool{OpBuffers: true})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := readBuffers(ops[0].Payload)
	if !ok || len(got) != 2 || got[1].version != 65536 {
		t.Fatalf("round trip gave %+v (ok=%v)", got, ok)
	}
}

// A truncated payload must not invent fields. Bad is checked once at the end
// rather than after each read, so this is the test that the flag actually
// propagates through a whole record.
func TestTruncatedRecordSetsBad(t *testing.T) {
	full := writeBuffers([]buffer{{"/w/a.go", 41, true}, {"/w/b.go", 9, false}}).Payload
	for cut := 1; cut < len(full); cut++ {
		got, ok := readBuffers(full[:cut])
		if ok && len(got) == 2 {
			t.Errorf("a payload cut to %d bytes read as two whole buffers", cut)
		}
		for _, b := range got {
			if b.path != "/w/a.go" && b.path != "/w/b.go" && b.path != "" {
				t.Errorf("cut %d produced a corrupt path %q", cut, b.path)
			}
		}
	}
}

// Reading past the end yields zero values rather than panicking, which is what
// lets a caller check Bad once instead of at every field.
func TestReadingPastTheEndIsSafe(t *testing.T) {
	r := NewReader(nil)
	if r.Num() != 0 || r.Str() != "" || r.Bool() {
		t.Error("an empty payload produced non-zero fields")
	}
	if !r.Bad {
		t.Error("reading past the end did not set Bad")
	}
	if r.More() {
		t.Error("More stayed true after a failure")
	}
}
