package journal

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Every kind round-trips, including the shapes the format has to name even
// when empty — a zero-length blob, an op with no pieces on either side — and
// the largest value each field can carry.
func TestCodecRoundTripEveryKind(t *testing.T) {
	max := ^uint64(0)
	cases := []Record{
		Base{Path: "internal/piecetable/oplog.go", Hash: "sha256:abc", Bytes: []byte("package piecetable\n")},
		Base{Path: "", Hash: "", Bytes: nil},
		Base{Path: "empty.go", Hash: "sha256:def", Bytes: []byte{}},
		StoreAppend{Author: 0, Start: 0, Blob: nil},
		StoreAppend{Author: 255, Start: max, Blob: []byte("appended text")},
		Op{},
		Op{Seq: max, Author: 255, Pos: max,
			Del:    []Piece{{Buf: 1, Start: max, Length: max}},
			Ins:    []Piece{{Buf: 0, Start: 0, Length: 0}},
			Undoes: max, Group: max, Kind: OpRedo},
		Op{Seq: 1, Author: 2, Pos: 3, Del: nil,
			Ins: []Piece{{Buf: 3, Start: 4, Length: 5}}, Group: 7, Kind: OpUndo},
		Decision{Group: 0, State: StateAccepted},
		Decision{Group: 99, State: StateProposed},
		Decision{Group: max, State: StateRejected},
		Author{ID: 0, Identity: "", Name: "", Kind: Human},
		Author{ID: 255, Identity: "tok_abc", Name: "claude", Kind: Agent},
		Session{Blob: nil},
		Session{Blob: []byte(`{"tabs":[]}`)},
		Written{Path: "a.go", Hash: "sha256:abc", Version: 7},
		Written{Path: "b.go", Hash: "sha256:def", Version: max},
		Written{},
		Base{Path: "u16.go", Hash: "sha256:enc", Bytes: []byte("a\x00b"),
			Encoding: Encoding{Kind: EncodingUTF16LE, BOM: true}},
		Base{Path: "cp.go", Hash: "sha256:enc", Encoding: Encoding{Kind: EncodingWindows1252, CRLF: true}},
		Written{Path: "latin.go", Hash: "sha256:enc", Version: 12, Encoding: Encoding{Kind: EncodingLatin1, CRLF: true}},
		Written{Path: "bom.go", Hash: "sha256:enc", Version: 13, Encoding: Encoding{BOM: true}},
	}
	for i, want := range cases {
		kind, payload, err := encodeRecord(want)
		if err != nil {
			t.Fatalf("case %d: encode: %v", i, err)
		}
		if kind != want.recordKind() {
			t.Fatalf("case %d: kind = %v, want %v", i, kind, want.recordKind())
		}
		got, err := decodeRecord(kind, payload)
		if err != nil {
			t.Fatalf("case %d: decode: %v", i, err)
		}
		if !equalRecord(got, want) {
			t.Fatalf("case %d: round trip = %#v, want %#v", i, got, want)
		}
	}
}

// The encoding a save recorded survives the codec on both records that carry
// it, so a restore can re-encode UTF-16, a BOM or CRLF exactly as the file was
// written. The zero value is not a special case: it is the default, and a
// record that carries it omits it on the wire.
func TestEncodingRoundTripsThroughBaseAndWritten(t *testing.T) {
	cases := []struct {
		name string
		rec  Record
		want Encoding
	}{
		{"base utf16le", Base{Path: "u16.go", Hash: "h", Bytes: []byte("a\x00b"),
			Encoding: Encoding{Kind: EncodingUTF16LE, BOM: true}},
			Encoding{Kind: EncodingUTF16LE, BOM: true}},
		{"base windows1252 crlf", Base{Path: "cp.go", Hash: "h",
			Encoding: Encoding{Kind: EncodingWindows1252, CRLF: true}},
			Encoding{Kind: EncodingWindows1252, CRLF: true}},
		{"written utf8 bom", Written{Path: "b.go", Hash: "h", Version: 3,
			Encoding: Encoding{BOM: true}},
			Encoding{BOM: true}},
		{"written latin1", Written{Path: "l.go", Hash: "h", Version: 9,
			Encoding: Encoding{Kind: EncodingLatin1}},
			Encoding{Kind: EncodingLatin1}},
		{"written default omitted", Written{Path: "d.go", Hash: "h", Version: 1}, Encoding{}},
	}
	for _, tc := range cases {
		kind, payload, err := encodeRecord(tc.rec)
		if err != nil {
			t.Fatalf("%s: encode: %v", tc.name, err)
		}
		got, err := decodeRecord(kind, payload)
		if err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		if !equalRecord(got, tc.rec) {
			t.Fatalf("%s: round trip = %#v, want %#v", tc.name, got, tc.rec)
		}
		if enc := encodingOf(got); enc != tc.want {
			t.Fatalf("%s: encoding = %+v, want %+v", tc.name, enc, tc.want)
		}
	}
}

// A record written before the encoding field existed still decodes, and the
// missing tail reads as the default — UTF-8, LF, no BOM. The old layout is a
// prefix of the new one, so an older log's Base and Written decode without a
// version branch or a hand-rolled upgrade, and nothing is invented for a file
// whose shape was never recorded.
func TestOlderRecordsWithoutEncodingDecodeAsDefault(t *testing.T) {
	e := &encoder{}
	e.str("old.go")
	e.str("h")
	e.blob([]byte("old body"))
	if e.err != nil {
		t.Fatalf("build old base: %v", e.err)
	}
	got, err := decodeRecord(KindBase, e.b)
	if err != nil {
		t.Fatalf("decode old base: %v", err)
	}
	if base, ok := got.(Base); !ok || base.Encoding != (Encoding{}) {
		t.Fatalf("old base encoding = %+v, want the zero default", got)
	}

	e = &encoder{}
	e.str("old.go")
	e.str("h")
	e.u64(7)
	if e.err != nil {
		t.Fatalf("build old written: %v", e.err)
	}
	got, err = decodeRecord(KindWritten, e.b)
	if err != nil {
		t.Fatalf("decode old written: %v", err)
	}
	if mark, ok := got.(Written); !ok || mark.Encoding != (Encoding{}) {
		t.Fatalf("old written encoding = %+v, want the zero default", got)
	}
}

// The whole-file path agrees with the codec: a log whose records predate the
// encoding field opens, and Log.Encoding reports the default rather than
// refusing the file or inventing a value.
func TestOpenReadsAnOlderLogAsDefaultEncoding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raj.log")
	w, err := Create(path, Header{Root: "/work"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := w.Append(Base{Path: "old.go", Hash: "h", Bytes: []byte("old")}); err != nil {
		t.Fatalf("Append base: %v", err)
	}
	if err := w.Append(Written{Path: "old.go", Hash: "h", Version: 4}); err != nil {
		t.Fatalf("Append written: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if l.Damaged {
		t.Fatalf("log damaged at %d of %d", l.Tail, l.Size)
	}
	if enc := l.Encoding(); enc != (Encoding{}) {
		t.Fatalf("Log.Encoding = %+v, want the zero default", enc)
	}
}

// Log.Encoding reports the shape of the bytes the buffer must re-encode with:
// the last Written marker's encoding when the log saved, otherwise the base's.
// A restore path needs exactly this one call.
func TestLogEncodingFollowsTheLastWrite(t *testing.T) {
	write := func(t *testing.T, recs ...Record) *Log {
		t.Helper()
		path := filepath.Join(t.TempDir(), "raj.log")
		w, err := Create(path, Header{Root: "/work"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		for _, r := range recs {
			if err := w.Append(r); err != nil {
				t.Fatalf("Append: %v", err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		l, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if l.Damaged {
			t.Fatalf("log damaged at %d of %d", l.Tail, l.Size)
		}
		return l
	}
	u16 := Encoding{Kind: EncodingUTF16LE, BOM: true}
	latin := Encoding{Kind: EncodingLatin1, CRLF: true}
	if got := write(t, Base{Path: "f.go", Encoding: u16}).Encoding(); got != u16 {
		t.Fatalf("base-only encoding = %+v, want %+v", got, u16)
	}
	if got := write(t,
		Base{Path: "f.go", Encoding: u16},
		Written{Path: "f.go", Version: 3, Encoding: latin},
	).Encoding(); got != latin {
		t.Fatalf("last-write encoding = %+v, want %+v", got, latin)
	}
}

// A corrupt encoding is refused the way the format's other enums are: validate
// rejects an unknown kind, so decodeRecord returns an error and Open refuses
// the log rather than restoring a shape this build cannot reproduce. The flags
// byte's unknown bits and an explicit default are refused for the same reason:
// neither is a canonical record.
func TestCodecRejectsAnUnknownEncoding(t *testing.T) {
	build := func(t *testing.T, kind EncodingKind, flags uint8) []byte {
		t.Helper()
		e := &encoder{}
		e.str("f.go")
		e.str("h")
		e.blob([]byte("body"))
		e.u8(uint8(kind))
		e.u8(flags)
		if e.err != nil {
			t.Fatalf("build: %v", e.err)
		}
		return e.b
	}
	cases := []struct {
		name  string
		kind  EncodingKind
		flags uint8
	}{
		{"unknown kind", EncodingKind(200), 0},
		{"unknown flags", EncodingUTF16LE, 0x80},
		{"explicit default", EncodingUTF8, 0},
	}
	for _, tc := range cases {
		if _, err := decodeRecord(KindBase, build(t, tc.kind, tc.flags)); err == nil {
			t.Fatalf("%s: decode accepted a non-canonical encoding", tc.name)
		}
	}
}

func encodingOf(r Record) Encoding {
	switch v := r.(type) {
	case Base:
		return v.Encoding
	case Written:
		return v.Encoding
	}
	return Encoding{}
}

// Append a run of records, reopen, and read every one back in order with the
// header intact.
func TestAppendReopenReadsAllInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raj.log")
	w, err := Create(path, Header{Root: "/work", Identity: "tok_host"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var want []Record
	for i := 0; i < 64; i++ {
		var r Record
		switch i % 6 {
		case 0:
			r = Base{Path: fmt.Sprintf("f%d.go", i), Hash: "h", Bytes: []byte(fmt.Sprintf("body %d", i))}
		case 1:
			r = StoreAppend{Author: uint8(i % 4), Start: uint64(i * 8), Blob: []byte(fmt.Sprintf("blob %d", i))}
		case 2:
			r = Op{Seq: uint64(i), Author: uint8(i % 4), Pos: uint64(i * 3),
				Del:   []Piece{{Buf: 1, Start: uint64(i), Length: 2}},
				Ins:   []Piece{{Buf: 2, Start: uint64(i), Length: 1}},
				Group: uint64(i), Kind: OpEdit}
		case 3:
			r = Decision{Group: uint64(i), State: StateProposed}
		case 4:
			r = Author{ID: uint8(i), Identity: fmt.Sprintf("id-%d", i), Name: "a", Kind: Agent}
		default:
			r = Session{Blob: []byte(fmt.Sprintf("session %d", i))}
		}
		if err := w.Append(r); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		want = append(want, r)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if l.Damaged {
		t.Fatalf("log damaged at %d of %d", l.Tail, l.Size)
	}
	if l.Header != (Header{Root: "/work", Identity: "tok_host"}) {
		t.Fatalf("header = %+v", l.Header)
	}
	if len(l.Records) != len(want) {
		t.Fatalf("read %d records, want %d", len(l.Records), len(want))
	}
	for i := range want {
		if !equalRecord(l.Records[i], want[i]) {
			t.Fatalf("record %d = %#v, want %#v", i, l.Records[i], want[i])
		}
	}
	if l.Tail != l.Size {
		t.Fatalf("Tail = %d, want the whole file %d", l.Tail, l.Size)
	}
}

// A record cut in half opens as the records before it, reports the boundary,
// and truncating there lets appending continue.
func TestTruncatedTailKeepsTheValidPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raj.log")
	w, err := Create(path, Header{Root: "/work"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	prefix := []Record{
		Base{Path: "a.go", Hash: "h", Bytes: []byte("a")},
		StoreAppend{Author: 3, Start: 0, Blob: []byte("appended")},
		Decision{Group: 1, State: StateRejected},
	}
	for _, r := range prefix {
		if err := w.Append(r); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	boundary := size(t, path)
	late := Op{Seq: 9, Author: 4, Pos: 2,
		Del: []Piece{{Buf: 1, Start: 0, Length: 1}}, Group: 9, Kind: OpEdit}
	if err := w.Append(late); err != nil {
		t.Fatalf("Append late: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	end := size(t, path)
	if err := Truncate(path, boundary+(end-boundary)/2); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !l.Damaged {
		t.Fatal("a torn record read as clean")
	}
	if l.Tail != boundary {
		t.Fatalf("Tail = %d, want the record boundary %d", l.Tail, boundary)
	}
	if len(l.Records) != len(prefix) {
		t.Fatalf("read %d records, want the %d before the tear", len(l.Records), len(prefix))
	}
	for i := range prefix {
		if !equalRecord(l.Records[i], prefix[i]) {
			t.Fatalf("prefix record %d changed", i)
		}
	}
	// Repair, then appending continues from the clean boundary.
	if err := l.Truncate(); err != nil {
		t.Fatalf("Log.Truncate: %v", err)
	}
	if got := size(t, path); got != boundary {
		t.Fatalf("size after repair = %d, want %d", got, boundary)
	}
	w2, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter after repair: %v", err)
	}
	if err := w2.Append(Decision{Group: 42}); err != nil {
		t.Fatalf("Append after repair: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("Close after repair: %v", err)
	}
	l2, err := Open(path)
	if err != nil {
		t.Fatalf("Open after repair: %v", err)
	}
	if l2.Damaged || len(l2.Records) != len(prefix)+1 {
		t.Fatalf("after repair: damaged=%v records=%d, want clean with %d",
			l2.Damaged, len(l2.Records), len(prefix)+1)
	}
}

// OpenWriter refuses to bury the valid prefix behind a damaged tail.
func TestOpenWriterRefusesADamagedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raj.log")
	w, err := Create(path, Header{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := w.Append(Base{Path: "a"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.Write([]byte{0, 0, 0}); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	f.Close()
	if _, err := OpenWriter(path); !errors.Is(err, ErrDamaged) {
		t.Fatalf("OpenWriter = %v, want ErrDamaged", err)
	}
}

// A single flipped byte inside a record is a bad checksum, so the record is
// dropped rather than decoded as a different value.
func TestFlippedByteIsDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raj.log")
	w, err := Create(path, Header{Root: "/work"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	first := Base{Path: "a.go", Hash: "h", Bytes: []byte("first record")}
	if err := w.Append(first); err != nil {
		t.Fatalf("Append first: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	second := Author{ID: 3, Identity: "tok_x", Name: "claude", Kind: Agent}
	secondAt := size(t, path)
	if err := w.Append(second); err != nil {
		t.Fatalf("Append second: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	i := int(secondAt) + recordHeaderLen + 4 // inside the second record's payload
	data[i] ^= 0x40
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !l.Damaged {
		t.Fatal("a flipped payload byte read as valid")
	}
	if l.Tail != secondAt {
		t.Fatalf("Tail = %d, want the damaged record offset %d", l.Tail, secondAt)
	}
	if len(l.Records) != 1 || !equalRecord(l.Records[0], first) {
		t.Fatalf("records = %#v, want only the first", l.Records)
	}
}

// A foreign file and a future version are errors, not damaged logs.
func TestOpenRejectsForeignAndUnknownFiles(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "foreign")
	if err := os.WriteFile(foreign, []byte("this is definitely not a journal file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(foreign); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("Open(foreign) = %v, want ErrBadMagic", err)
	}
	path := filepath.Join(dir, "version")
	w, err := Create(path, Header{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(Magic)] = 99 // bump the format version
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrVersion) {
		t.Fatalf("Open(version) = %v, want ErrVersion", err)
	}
}

// The encoder refuses a string past its advertised limit rather than writing a
// length a reader would then refuse.
func TestEncoderRejectsAnOversizeString(t *testing.T) {
	big := strings.Repeat("x", MaxStringBytes+1)
	if _, _, err := encodeRecord(Base{Path: big}); err == nil {
		t.Fatal("a path past MaxStringBytes encoded")
	}
}

// A deterministic randomized run over every kind, as a companion to the fuzz
// target so a failure is reproducible from the seed alone.
func TestCodecRandomized(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 5000; i++ {
		want := randomRecord(rnd)
		kind, payload, err := encodeRecord(want)
		if err != nil {
			t.Fatalf("case %d: encode %#v: %v", i, want, err)
		}
		got, err := decodeRecord(kind, payload)
		if err != nil {
			t.Fatalf("case %d: decode %#v: %v", i, want, err)
		}
		if !equalRecord(got, want) {
			t.Fatalf("case %d: %#v != %#v", i, got, want)
		}
	}
}

// FuzzDecodeRecord asserts the codec never panics and is canonical: anything
// that decodes must re-encode to the exact bytes it came from.
func FuzzDecodeRecord(f *testing.F) {
	seeds := []Record{
		Base{Path: "p", Hash: "h", Bytes: []byte("b"),
			Encoding: Encoding{Kind: EncodingUTF16LE, BOM: true}},
		Written{Path: "w", Hash: "h", Version: 2,
			Encoding: Encoding{Kind: EncodingWindows1252, CRLF: true}},
		StoreAppend{Author: 1, Start: 2, Blob: []byte("x")},
		Op{Seq: 1, Author: 2, Pos: 3,
			Del:    []Piece{{Buf: 1, Start: 4, Length: 5}},
			Ins:    []Piece{{Buf: 2, Start: 6, Length: 7}},
			Undoes: 8, Group: 9, Kind: OpUndo},
		Decision{Group: 10, State: StateRejected},
		Author{ID: 11, Identity: "i", Name: "n", Kind: Agent},
		Session{Blob: []byte("s")},
	}
	for _, r := range seeds {
		kind, payload, err := encodeRecord(r)
		if err != nil {
			f.Fatalf("seed: %v", err)
		}
		f.Add(uint8(kind), payload)
	}
	f.Fuzz(func(t *testing.T, k uint8, data []byte) {
		kind := RecordKind(k)
		r, err := decodeRecord(kind, data)
		if err != nil {
			return
		}
		kind2, out, err := encodeRecord(r)
		if err != nil {
			t.Fatalf("re-encode %#v: %v", r, err)
		}
		if kind2 != kind {
			t.Fatalf("kind changed %v -> %v", kind, kind2)
		}
		if !bytes.Equal(out, data) {
			t.Fatalf("non-canonical: %x decoded to %#v and back as %x", data, r, out)
		}
	})
}

// FuzzDecodeHeader is the same canonicality check for the header body.
func FuzzDecodeHeader(f *testing.F) {
	body, err := encodeHeader(Header{Root: "/work", Identity: "tok"})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(body)
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := decodeHeader(data)
		if err != nil {
			return
		}
		out, err := encodeHeader(got)
		if err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		if !bytes.Equal(out, data) {
			t.Fatalf("non-canonical header: %x -> %#v -> %x", data, got, out)
		}
	})
}

func randomRecord(rnd *rand.Rand) Record {
	author := uint8(rnd.Intn(256))
	switch rnd.Intn(7) {
	case 0:
		return Base{Path: randomString(rnd), Hash: randomString(rnd), Bytes: randomBytes(rnd),
			Encoding: randomEncoding(rnd)}
	case 1:
		return StoreAppend{Author: author, Start: rnd.Uint64(), Blob: randomBytes(rnd)}
	case 2:
		return Op{Seq: rnd.Uint64(), Author: author, Pos: rnd.Uint64(),
			Del: randomPieces(rnd), Ins: randomPieces(rnd),
			Undoes: rnd.Uint64(), Group: rnd.Uint64(), Kind: OpKind(rnd.Intn(3))}
	case 3:
		return Decision{Group: rnd.Uint64(), State: GroupState(rnd.Intn(3))}
	case 4:
		return Author{ID: author, Identity: randomString(rnd), Name: randomString(rnd),
			Kind: ParticipantKind(rnd.Intn(2))}
	case 5:
		return Session{Blob: randomBytes(rnd)}
	default:
		return Written{Path: randomString(rnd), Hash: randomString(rnd), Version: rnd.Uint64(),
			Encoding: randomEncoding(rnd)}
	}
}

func randomEncoding(rnd *rand.Rand) Encoding {
	return Encoding{
		Kind: EncodingKind(rnd.Intn(int(EncodingLatin1) + 1)),
		CRLF: rnd.Intn(2) == 0,
		BOM:  rnd.Intn(2) == 0,
	}
}

func randomString(rnd *rand.Rand) string { return string(randomBytes(rnd)) }

func randomBytes(rnd *rand.Rand) []byte {
	b := make([]byte, rnd.Intn(37))
	rnd.Read(b)
	return b
}

func randomPieces(rnd *rand.Rand) []Piece {
	n := rnd.Intn(5)
	if n == 0 {
		return nil
	}
	out := make([]Piece, n)
	for i := range out {
		out[i] = Piece{Buf: uint8(rnd.Intn(256)), Start: rnd.Uint64(), Length: rnd.Uint64()}
	}
	return out
}

// canonical makes nil and empty slices equal, the one distinction the wire
// format does not preserve.
func canonical(r Record) Record {
	switch v := r.(type) {
	case Base:
		v.Bytes = orEmpty(v.Bytes)
		return v
	case StoreAppend:
		v.Blob = orEmpty(v.Blob)
		return v
	case Op:
		v.Del = orEmptyPieces(v.Del)
		v.Ins = orEmptyPieces(v.Ins)
		return v
	case Session:
		v.Blob = orEmpty(v.Blob)
		return v
	}
	return r
}

func orEmpty(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func orEmptyPieces(p []Piece) []Piece {
	if p == nil {
		return []Piece{}
	}
	return p
}

func equalRecord(a, b Record) bool {
	return reflect.DeepEqual(canonical(a), canonical(b))
}

func size(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	return info.Size()
}
