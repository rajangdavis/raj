package prog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func allKnown() map[byte]bool {
	m := map[byte]bool{}
	for code := range names {
		m[code] = true
	}
	return m
}

func TestRoundTrip(t *testing.T) {
	ops := []Op{
		{OpID, Number(7)},
		{OpPath, []byte("/tmp/x.go")},
		{OpBase, Number(3)},
		{OpSpan, Pair(0, 4)},
		{OpText, []byte("hello")},
		{OpApply, nil},
	}
	got, err := Decode(Encode(ops), allKnown())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ops) {
		t.Fatalf("decoded %d ops, want %d", len(got), len(ops))
	}
	for i := range ops {
		if got[i].Code != ops[i].Code || !bytes.Equal(got[i].Payload, ops[i].Payload) {
			t.Errorf("op %d = %v %q, want %v %q",
				i, got[i].Code, got[i].Payload, ops[i].Code, ops[i].Payload)
		}
	}
}

// The forward-compatibility rule, which is the whole reason opcodes live in two
// ranges: a qualification you do not understand costs precision, a request you
// do not understand costs correctness.
func TestUnknownArgumentIsSkipped(t *testing.T) {
	b := Encode([]Op{
		{0x7f, []byte("from a later raj")}, // an argument this build never heard of
		{OpPath, []byte("x.go")},
		{OpOpen, nil},
	})
	ops, err := Decode(b, allKnown())
	if err != nil {
		t.Fatalf("an unknown argument was fatal: %v", err)
	}
	if len(ops) != 2 || ops[0].Code != OpPath || ops[1].Code != OpOpen {
		t.Fatalf("ops = %+v, want the path and the verb", ops)
	}
}

func TestUnknownVerbIsRefused(t *testing.T) {
	b := Encode([]Op{{OpPath, []byte("x.go")}, {0xff, nil}})
	_, err := Decode(b, allKnown())
	if !errors.Is(err, ErrUnknownVerb) {
		t.Fatalf("Decode = %v, want ErrUnknownVerb", err)
	}
	if !strings.Contains(err.Error(), "verb") {
		t.Errorf("error %q does not say which range the opcode was in", err)
	}
}

// A batch is a repetition rather than a new shape: fifty splices in one frame,
// where the JSON header needed fifty frames.
func TestBatchOfApplies(t *testing.T) {
	var ops []Op
	for i := 0; i < 50; i++ {
		ops = append(ops,
			Op{OpSpan, Pair(i, i+1)},
			Op{OpText, []byte("x")},
			Op{OpApply, nil})
	}
	got, err := Decode(Encode(ops), allKnown())
	if err != nil {
		t.Fatal(err)
	}
	verbs := 0
	for _, op := range got {
		if IsVerb(op.Code) {
			verbs++
		}
	}
	if verbs != 50 {
		t.Errorf("decoded %d verbs, want 50", verbs)
	}
}

// A filename on Linux is arbitrary bytes. This is the case that forced PathLen
// into the JSON header, and the reason it is gone.
func TestPathThatIsNotUTF8SurvivesIntact(t *testing.T) {
	name := []byte("caf\xe9.txt") // Latin-1, a real filename
	ops, err := Decode(Encode([]Op{{OpPath, name}, {OpOpen, nil}}), allKnown())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ops[0].Payload, name) {
		t.Fatalf("path = %q, want %q", ops[0].Payload, name)
	}
	// And the contrast that motivates it: JSON does not manage this.
	if b, err := json.Marshal(string(name)); err == nil {
		var back string
		json.Unmarshal(b, &back)
		if back == string(name) {
			t.Skip("this Go encodes invalid UTF-8 losslessly; the motivation stands but not the demo")
		}
	}
}

func TestVarintRoundTrip(t *testing.T) {
	for _, v := range []int{0, 1, 126, 127, 128, 255, 16383, 16384, 1 << 20, 1 << 31} {
		b := PutVarint(nil, v)
		got, n, err := Varint(b)
		if err != nil {
			t.Fatalf("%d: %v", v, err)
		}
		if got != v || n != len(b) {
			t.Errorf("%d round-tripped as %d in %d/%d bytes", v, got, n, len(b))
		}
		if n != VarintLen(v) {
			t.Errorf("%d: VarintLen said %d, encoding took %d", v, VarintLen(v), n)
		}
	}
}

// The property the encoding is shaped around: nothing in the framing is ever
// zero, so a program travels through anything that stops at NUL — argv being
// the one that matters.
func TestFramingNeverContainsZero(t *testing.T) {
	for _, n := range []int{0, 1, 126, 127, 128, 300, 70000} {
		for _, code := range []byte{OpText, OpApply, OpPath} {
			payload := bytes.Repeat([]byte("x"), n) // printable, so a zero is framing
			b := Encode([]Op{{code, payload}})
			if i := bytes.IndexByte(b, 0); i >= 0 {
				t.Fatalf("%s with a %d-byte payload put a zero at %d", Name(code), n, i)
			}
			ops, err := Decode(b, allKnown())
			if err != nil {
				t.Fatalf("payload %d: %v", n, err)
			}
			if len(ops[0].Payload) != n {
				t.Errorf("payload %d: decoded %d bytes", n, len(ops[0].Payload))
			}
		}
	}
}

// Numbers are framing too, and a fixed-width 256 would have been 00 01.
func TestNumbersNeverContainZero(t *testing.T) {
	for _, v := range []int{0, 1, 127, 128, 255, 256, 65536, 1 << 24} {
		b := Encode([]Op{{OpBase, Number(v)}, {OpApply, nil}})
		if bytes.IndexByte(b, 0) >= 0 {
			t.Errorf("base %d produced a zero byte: %x", v, b)
		}
		ops, _ := Decode(b, allKnown())
		if got := ReadNumber(ops[0].Payload); got != v {
			t.Errorf("base %d read back as %d", v, got)
		}
	}
	if start, end := ReadPair(Pair(0, 70000)); start != 0 || end != 70000 {
		t.Errorf("pair read back as (%d,%d)", start, end)
	}
}

// A zero byte in the framing is what a C string does to a program, so it gets
// its own error rather than a generic one.
func TestZeroByteInFramingIsNamed(t *testing.T) {
	b := []byte{Magic, Version, OpText, 0x00}
	if _, err := Decode(b, allKnown()); !errors.Is(err, ErrNulByte) {
		t.Errorf("Decode = %v, want ErrNulByte", err)
	}
	if !HasNul(b) {
		t.Error("HasNul missed a zero byte")
	}
	if HasNul(Encode([]Op{{OpText, []byte("hi")}, {OpApply, nil}})) {
		t.Error("HasNul flagged a program with no zero byte")
	}
}

func TestTruncatedProgramsAreRefused(t *testing.T) {
	full := Encode([]Op{{OpPath, []byte("some/path.go")}, {OpOpen, nil}})
	want, err := Decode(full, allKnown())
	if err != nil {
		t.Fatal(err)
	}
	// A cut landing exactly on an op boundary is a shorter valid program, not a
	// corrupt one — that is what length prefixes buy. So the property is that a
	// truncation either fails or loses ops; what it must never do is report the
	// whole program from part of the bytes.
	for cut := 0; cut < len(full); cut++ {
		got, err := Decode(full[:cut], allKnown())
		if err != nil {
			continue
		}
		if len(got) >= len(want) {
			t.Errorf("a program cut to %d bytes still decoded %d ops", cut, len(got))
		}
	}
}

// A length field can say more than the frame holds, and the frame comes off a
// socket. This is a bounds check, not an assertion.
func TestLengthPastTheEndIsRefused(t *testing.T) {
	b := PutVarint([]byte{Magic, Version, OpText}, 200)
	b = append(b, 'h', 'i')
	if _, err := Decode(b, allKnown()); !errors.Is(err, ErrShort) {
		t.Fatalf("Decode = %v, want ErrShort", err)
	}
}

// Decode must not panic, whatever arrives on the socket.
func FuzzDecode(f *testing.F) {
	f.Add(Encode([]Op{{OpPath, []byte("a")}, {OpOpen, nil}}))
	f.Add([]byte{Magic, Version})
	f.Add([]byte{Magic, Version, OpText, 0x00})
	f.Add([]byte("R\x01\x84\x01"))
	f.Fuzz(func(t *testing.T, b []byte) {
		ops, err := Decode(b, allKnown())
		if err != nil {
			return
		}
		// Anything that decodes must re-encode to something that decodes to the
		// same ops. Not byte equality: a program may have been written with a
		// wider width than its payloads need, and narrowing it is correct.
		again, err := Decode(Encode(ops), allKnown())
		if err != nil {
			t.Fatalf("re-encoding a valid program produced an invalid one: %v", err)
		}
		if len(again) != len(ops) {
			t.Fatalf("round trip changed the op count: %d -> %d", len(ops), len(again))
		}
		for i := range ops {
			if again[i].Code != ops[i].Code || !bytes.Equal(again[i].Payload, ops[i].Payload) {
				t.Fatalf("op %d changed across a round trip", i)
			}
		}
	})
}

// --- the width question -----------------------------------------------------

// programShapes are the three request shapes that actually cross this wire, so
// the benchmark measures the choice rather than a synthetic worst case.
func programShapes(rng *rand.Rand) map[string][]Op {
	big := make([]byte, 300<<10) // an agent rewriting a large file in one apply
	rng.Read(big)
	var batch []Op
	for i := 0; i < 50; i++ { // fifty small splices, the shape batching is for
		batch = append(batch,
			Op{OpSpan, Pair(i, i+2)},
			Op{OpText, []byte("fix")},
			Op{OpApply, nil})
	}
	return map[string][]Op{
		"small-open":  {{OpID, Number(1)}, {OpPath, []byte("internal/app/app.go")}, {OpOpen, nil}},
		"batch-apply": batch,
		"large-apply": {{OpPath, []byte("main.go")}, {OpBase, Number(4)}, {OpText, big}, {OpApply, nil}},
	}
}

// encodeFixed is the alternative: always four bytes of length, no width byte to
// choose. Kept here rather than in the package because it exists to be measured
// against, not to be used.
func encodeFixed(ops []Op) []byte {
	size := 3
	for _, op := range ops {
		size += 5 + len(op.Payload)
	}
	out := make([]byte, 0, size)
	out = append(out, Magic, Version, 4)
	for _, op := range ops {
		out = append(out, op.Code)
		n := uint32(len(op.Payload))
		out = append(out, byte(n), byte(n>>8), byte(n>>16), byte(n>>24))
		out = append(out, op.Payload...)
	}
	return out
}

// TestWidthCost reports what varint lengths buy, in bytes, for each shape.
// Run with -v; it asserts only that the narrow form is never larger.
func TestWidthCost(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for name, ops := range programShapes(rng) {
		narrow, fixed := len(Encode(ops)), len(encodeFixed(ops))
		saved := fixed - narrow
		pct := 100 * float64(saved) / float64(fixed)
		t.Logf("%-12s narrow=%-8d fixed=%-8d saved=%-6d (%.1f%%)", name, narrow, fixed, saved, pct)
		if narrow > fixed {
			t.Errorf("%s: varint lengths were larger than fixed u32", name)
		}
	}
}

func BenchmarkEncode(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	for name, ops := range programShapes(rng) {
		b.Run(name+"/narrow", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				sink = Encode(ops)
			}
			b.ReportMetric(float64(len(sink)), "wire_bytes")
		})
		b.Run(name+"/fixed", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				sink = encodeFixed(ops)
			}
			b.ReportMetric(float64(len(sink)), "wire_bytes")
		})
	}
}

func BenchmarkDecode(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	known := allKnown()
	for name, ops := range programShapes(rng) {
		encoded := Encode(ops)
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				out, err := Decode(encoded, known)
				if err != nil {
					b.Fatal(err)
				}
				opSink = out
			}
		})
	}
}

// BenchmarkAgainstJSON is the comparison that decides whether any of this is
// worth doing. The JSON side encodes the same request the way the current
// header does, with document bytes going in as a string.
func BenchmarkAgainstJSON(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	text := make([]byte, 64<<10)
	for i := range text {
		text[i] = byte('a' + rng.Intn(26))
	}
	ops := []Op{
		{OpID, Number(1)}, {OpPath, []byte("internal/app/app.go")},
		{OpBase, Number(4)}, {OpSpan, Pair(0, 16)}, {OpText, text}, {OpApply, nil},
	}
	type header struct {
		ID    int    `json:"id"`
		Op    string `json:"op"`
		Path  string `json:"path"`
		Base  uint64 `json:"base"`
		Start int    `json:"start"`
		End   int    `json:"end"`
		Text  string `json:"text"`
	}
	h := header{1, "apply", "internal/app/app.go", 4, 0, 16, string(text)}

	b.Run("prog", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			sink = Encode(ops)
		}
		b.ReportMetric(float64(len(sink)), "wire_bytes")
	})
	b.Run("json", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			out, err := json.Marshal(h)
			if err != nil {
				b.Fatal(err)
			}
			sink = out
		}
		b.ReportMetric(float64(len(sink)), "wire_bytes")
	})
	_ = fmt.Sprint
}

var (
	sink   []byte
	opSink []Op
)
