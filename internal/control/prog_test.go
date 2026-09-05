package control

import (
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
		{Code: prog.OpBase, Payload: []byte{7}},
	}
	for i := 0; i < 50; i++ {
		ops = append(ops,
			prog.Op{Code: prog.OpSpan, Payload: []byte{byte(i), byte(i + 1)}},
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
		{Code: prog.OpBase, Payload: []byte{1}},
		{Code: prog.OpSpan, Payload: []byte{0, 4}},
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
