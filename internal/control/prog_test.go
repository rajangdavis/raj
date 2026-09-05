package control

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"raj/internal/prog"
)

func TestProgramCompilesAVerb(t *testing.T) {
	p := prog.Encode([]prog.Op{
		{Code: prog.OpPath, Payload: []byte("/w/main.go")},
		{Code: prog.OpOpen},
	})
	reqs, err := Requests(p, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("compiled %d requests, want 1", len(reqs))
	}
	if reqs[0].Op != "open" || reqs[0].Path != "/w/main.go" {
		t.Errorf("request = %+v", reqs[0])
	}
	if reqs[0].Author != 3 {
		t.Errorf("author = %d, want the connection's 3", reqs[0].Author)
	}
}

// The reason to batch: fifty splices state the path and base once, and arrive
// in one frame.
func TestProgramBatchesApplies(t *testing.T) {
	ops := []prog.Op{
		{Code: prog.OpPath, Payload: []byte("/w/main.go")},
		{Code: prog.OpBase, Payload: prog.Number(7)},
	}
	for i := 0; i < 50; i++ {
		ops = append(ops,
			prog.Op{Code: prog.OpSpan, Payload: prog.Pair(i, i+1)},
			prog.Op{Code: prog.OpText, Payload: []byte("x")},
			prog.Op{Code: prog.OpApply})
	}
	reqs, err := Requests(prog.Encode(ops), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 50 {
		t.Fatalf("compiled %d requests, want 50", len(reqs))
	}
	for i, req := range reqs {
		if req.Path != "/w/main.go" {
			t.Fatalf("request %d lost the path", i)
		}
		if req.Base == nil || *req.Base != 7 {
			t.Fatalf("request %d lost the base", i)
		}
		if len(req.Hunks) != 1 || req.Hunks[i-i].Start != i {
			t.Fatalf("request %d hunk = %+v", i, req.Hunks)
		}
	}
}

// A hunk belongs to the verb that consumed it and must not leak into the next.
func TestProgramResetsHunksBetweenVerbs(t *testing.T) {
	p := prog.Encode([]prog.Op{
		{Code: prog.OpPath, Payload: []byte("/w/a.go")},
		{Code: prog.OpBase, Payload: prog.Number(1)},
		{Code: prog.OpSpan, Payload: prog.Pair(0, 4)},
		{Code: prog.OpText, Payload: []byte("hi")},
		{Code: prog.OpApply},
		{Code: prog.OpSave},
	})
	reqs, err := Requests(p, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("compiled %d requests, want 2", len(reqs))
	}
	if len(reqs[1].Hunks) != 0 {
		t.Errorf("the save inherited the apply's hunk: %+v", reqs[1].Hunks)
	}
	if reqs[1].Path != "/w/a.go" {
		t.Errorf("the save lost the sticky path")
	}
}

// The forward-compatibility rule, at the layer that acts on it.
func TestProgramSkipsUnknownArgumentAndRefusesUnknownVerb(t *testing.T) {
	ok := prog.Encode([]prog.Op{
		{Code: 0x6f, Payload: []byte("from a later raj")},
		{Code: prog.OpPath, Payload: []byte("/w/a.go")},
		{Code: prog.OpOpen},
	})
	if _, err := Requests(ok, 1); err != nil {
		t.Errorf("an unknown argument was fatal: %v", err)
	}

	bad := prog.Encode([]prog.Op{
		{Code: prog.OpPath, Payload: []byte("/w/a.go")},
		{Code: 0xfe},
	})
	if _, err := Requests(bad, 1); !errors.Is(err, prog.ErrUnknownVerb) {
		t.Errorf("Requests = %v, want ErrUnknownVerb", err)
	}
}

// Arguments with no verb are a caller that thinks it asked for something.
func TestProgramWithNoVerbIsRefused(t *testing.T) {
	p := prog.Encode([]prog.Op{{Code: prog.OpPath, Payload: []byte("/w/a.go")}})
	_, err := Requests(p, 1)
	if err == nil {
		t.Fatal("a program with no verb compiled")
	}
	if !strings.Contains(err.Error(), "verb") {
		t.Errorf("error %q does not say what was missing", err)
	}
}

// A path that is not valid UTF-8 is a real filename on Linux, and the case that
// forced PathLen into the JSON header.
func TestProgramCarriesANonUTF8Path(t *testing.T) {
	name := "/w/caf\xe9.txt"
	p := prog.Encode([]prog.Op{
		{Code: prog.OpPath, Payload: []byte(name)},
		{Code: prog.OpOpen},
	})
	reqs, err := Requests(p, 1)
	if err != nil {
		t.Fatal(err)
	}
	if reqs[0].Path != name {
		t.Errorf("path = %q, want %q", reqs[0].Path, name)
	}
}

// A frame carrying a program must survive the wire unchanged: the body is not
// claimed by any header length, which is the one thing Split would object to.
func TestProgramRoundTripsThroughAFrame(t *testing.T) {
	p := prog.Encode([]prog.Op{
		{Code: prog.OpPath, Payload: []byte("/w/a.go")},
		{Code: prog.OpOpen},
	})
	h, body := EncodeRequest(Request{Op: "prog", Program: p})
	got, err := DecodeRequest(Frame{Header: h, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Program) != string(p) {
		t.Fatalf("program changed across the wire")
	}
	if _, err := Requests(got.Program, 1); err != nil {
		t.Fatalf("the decoded program did not compile: %v", err)
	}
}

// Every verb the compiler names has to be one the handlers already answer;
// two lists that drift produce a request nothing runs.
func TestEveryVerbNameIsKnown(t *testing.T) {
	for code, name := range verbNames {
		if !knownOps[code] {
			t.Errorf("%s is in verbNames but not knownOps", name)
		}
	}
	for code := range knownOps {
		if !prog.IsVerb(code) {
			continue
		}
		if _, ok := verbNames[code]; !ok {
			t.Errorf("%s is a known verb with no op name", prog.Name(code))
		}
	}
}

// A program goes through argv, which is the reason its lengths and numbers are
// biased varints rather than fixed-width fields. argv carries every byte except
// NUL, so an encoding whose framing never emits one needs no hex and no temp
// file. This is the test that keeps that true.
func TestProgramFramingSurvivesArgv(t *testing.T) {
	// Payloads across the varint boundaries, plus numbers that a fixed-width
	// encoding would have written with a zero byte in them.
	for _, n := range []int{0, 127, 128, 300, 70000} {
		p := prog.Encode([]prog.Op{
			{Code: prog.OpPath, Payload: bytes.Repeat([]byte("x"), n)},
			{Code: prog.OpBase, Payload: prog.Number(256)},
			{Code: prog.OpSpan, Payload: prog.Pair(0, 65536)},
			{Code: prog.OpApply},
		})
		if prog.HasNul(p) {
			t.Fatalf("payload %d produced a program with a zero byte, which argv "+
				"would truncate", n)
		}
		if _, err := Requests(p, 1); err != nil {
			t.Fatalf("payload %d: %v", n, err)
		}
	}
}

// --- streaming inside a batch ------------------------------------------------

// A streaming verb marks its own last frame Final, which is right when it is
// the whole request and wrong when it is the first of two: a client that saw
// Final would stop reading while a verb was still to come. The batch owns that
// decision now, and this is the test that says so.
func TestSearchInsideAProgramDoesNotEndTheBatch(t *testing.T) {
	f := newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})
	c, err := Dial(f.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	p := prog.Encode([]prog.Op{
		{Code: prog.OpQuery, Payload: []byte("hello")},
		{Code: prog.OpSearch},
		{Code: prog.OpPath, Payload: []byte("/w/a.go")},
		{Code: prog.OpRead},
	})

	var batches int
	res, err := c.DoStream(Request{Op: "prog", Program: p}, func(m []SearchMatch) {
		batches++
	})
	if err != nil {
		t.Fatal(err)
	}
	if batches == 0 {
		t.Error("the search emitted no batches inside the program")
	}
	// The frame that ended the conversation is the read's, not the search's.
	if got := res.Text(); got != "hello\n" {
		t.Errorf("last response = %q, want the read's text — the search ended the batch early", got)
	}
}

// The same search as the only verb still ends the batch, because then it is the
// last one.
func TestSearchAsTheLastVerbEndsTheBatch(t *testing.T) {
	f := newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})
	c, err := Dial(f.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	p := prog.Encode([]prog.Op{
		{Code: prog.OpQuery, Payload: []byte("hello")},
		{Code: prog.OpSearch},
	})
	var batches int
	res, err := c.DoStream(Request{Op: "prog", Program: p}, func(m []SearchMatch) { batches++ })
	if err != nil {
		t.Fatal(err)
	}
	if batches == 0 {
		t.Error("no batches")
	}
	if !res.Final {
		t.Error("the last frame of a program ending in a search was not Final")
	}
}

// The search arguments are four optional ops that can arrive in any order, so
// none of them can be the one that allocates the query.
func TestSearchArgumentsCompileInAnyOrder(t *testing.T) {
	ops := []prog.Op{
		{Code: prog.OpFlags, Payload: []byte{prog.FlagRegex | prog.FlagWord}},
		{Code: prog.OpExclude, Payload: []byte("vendor/**")},
		{Code: prog.OpQuery, Payload: []byte("f.*o")},
		{Code: prog.OpInclude, Payload: []byte("*.go")},
		{Code: prog.OpSearch},
	}
	reqs, err := Requests(prog.Encode(ops), 1)
	if err != nil {
		t.Fatal(err)
	}
	q := reqs[0].Query
	if q == nil {
		t.Fatal("no query compiled")
	}
	if q.Text != "f.*o" || q.Include != "*.go" || q.Exclude != "vendor/**" {
		t.Errorf("query = %+v", q)
	}
	if !q.Regex || !q.Word || q.Case {
		t.Errorf("flags = regex:%v case:%v word:%v", q.Regex, q.Case, q.Word)
	}
}

// The four verbs that stay out, and the compiler refuses them by the ordinary
// unknown-verb rule rather than by a special case.
func TestVerbsThatStayOutOfPrograms(t *testing.T) {
	for _, code := range []byte{0x8d, 0x8e, 0x8f} { // unallocated verb range
		p := prog.Encode([]prog.Op{{Code: code}})
		if _, err := Requests(p, 1); !errors.Is(err, prog.ErrUnknownVerb) {
			t.Errorf("verb %#x = %v, want ErrUnknownVerb", code, err)
		}
	}
}
