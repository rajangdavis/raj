package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func read(t *testing.T, s string) (*Message, error) {
	t.Helper()
	return ReadMessage(bufio.NewReader(strings.NewReader(s)))
}

func frame(body string) string {
	return "Content-Length: " + itoa(len(body)) + "\r\n\r\n" + body
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// A message written by this package is readable by it, which is the minimum
// the framing has to guarantee.
func TestRoundTrip(t *testing.T) {
	req, err := NewRequest(7, "textDocument/hover", map[string]any{"x": 1})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteMessage(&buf, req); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMessage(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "textDocument/hover" {
		t.Errorf("method = %q", got.Method)
	}
	if id, ok := got.RequestID(); !ok || id != 7 {
		t.Errorf("id = %d, %v; want 7", id, ok)
	}
	if got.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", got.JSONRPC)
	}
}

// Content-Length is in bytes, not characters. Getting that wrong desynchronises
// the stream permanently, because every later frame is then read from the
// middle of the previous one — so a body of multi-byte text is the case to
// pin down.
func TestLengthIsBytesNotCharacters(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMessage(&buf, &Message{
		Method: "x", Params: json.RawMessage(`{"s":"日本語 😀"}`),
	}); err != nil {
		t.Fatal(err)
	}
	header, rest, _ := strings.Cut(buf.String(), "\r\n\r\n")
	want := "Content-Length: " + itoa(len(rest))
	if header != want {
		t.Errorf("header = %q, want %q — the length must count bytes", header, want)
	}

	// And two frames back to back read as two, not one and a half.
	buf.Reset()
	WriteMessage(&buf, &Message{Method: "first", Params: json.RawMessage(`{"s":"日本語"}`)})
	WriteMessage(&buf, &Message{Method: "second"})
	r := bufio.NewReader(&buf)
	a, err := ReadMessage(r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ReadMessage(r)
	if err != nil {
		t.Fatal(err)
	}
	if a.Method != "first" || b.Method != "second" {
		t.Errorf("got %q then %q", a.Method, b.Method)
	}
}

// A body containing the header delimiter must not be mistaken for the end of a
// frame. There is no delimiter to scan for, which is why the length is
// authoritative and the body is read with ReadFull.
func TestBodyContainingDelimiters(t *testing.T) {
	params := json.RawMessage(`{"s":"Content-Length: 5\r\n\r\nfake"}`)
	var buf bytes.Buffer
	WriteMessage(&buf, &Message{Method: "x", Params: params})
	WriteMessage(&buf, &Message{Method: "after"})

	r := bufio.NewReader(&buf)
	if m, err := ReadMessage(r); err != nil || m.Method != "x" {
		t.Fatalf("first frame: %v, %v", m, err)
	}
	m, err := ReadMessage(r)
	if err != nil || m.Method != "after" {
		t.Errorf("the stream desynchronised: %v, %v", m, err)
	}
}

// Unknown headers are skipped rather than rejected: Content-Type is in the
// specification and servers have sent others, and failing against a server
// that is merely chattier than expected is not a failure worth having.
func TestUnknownHeadersAreSkipped(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1}`
	s := "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n" +
		"X-Something: whatever\r\n" +
		"Content-Length: " + itoa(len(body)) + "\r\n\r\n" + body

	got, err := read(t, s)
	if err != nil {
		t.Fatalf("unknown headers rejected: %v", err)
	}
	if id, ok := got.RequestID(); !ok || id != 1 {
		t.Errorf("id = %d, %v; want 1", id, ok)
	}
}

// Header names are case-insensitive, as in HTTP.
func TestHeaderNameCaseInsensitive(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":2}`
	for _, name := range []string{"Content-Length", "content-length", "CONTENT-LENGTH"} {
		s := name + ": " + itoa(len(body)) + "\r\n\r\n" + body
		if _, err := read(t, s); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
}

// A frame that announces more than the limit is refused before anything is
// allocated for it: a server announcing a gigabyte is broken or hostile, and
// allocating first is how it takes the editor down too.
func TestOversizedFrameIsRefused(t *testing.T) {
	s := "Content-Length: 999999999999\r\n\r\n"
	if _, err := read(t, s); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}

// Malformed frames produce errors rather than partial messages or panics.
func TestMalformedFrames(t *testing.T) {
	cases := map[string]string{
		"no length":        "Content-Type: x\r\n\r\n{}",
		"bad length":       "Content-Length: abc\r\n\r\n{}",
		"negative length":  "Content-Length: -5\r\n\r\n{}",
		"malformed header": "not a header\r\n\r\n{}",
		"truncated body":   "Content-Length: 100\r\n\r\n{}",
		"invalid json":     frame("{not json"),
		"empty":            "",
	}
	for name, s := range cases {
		if _, err := read(t, s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A clean end of stream is io.EOF, so a caller can tell a server that exited
// from one that sent garbage.
func TestCleanEOF(t *testing.T) {
	if _, err := read(t, ""); !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want io.EOF", err)
	}
}

// A response is distinguishable from a request the server originated, since
// both carry an ID and only one expects an answer.
func TestResponseVersusServerRequest(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":3,"result":{"ok":true}}`
	resp, err := read(t, frame(body))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsResponse() {
		t.Error("a result message is not recognised as a response")
	}

	body = `{"jsonrpc":"2.0","id":4,"method":"workspace/configuration"}`
	srv, err := read(t, frame(body))
	if err != nil {
		t.Fatal(err)
	}
	if srv.IsResponse() {
		t.Error("a server request was taken for a response")
	}

	body = `{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics"}`
	note, err := read(t, frame(body))
	if err != nil {
		t.Fatal(err)
	}
	if note.IsResponse() {
		t.Error("a notification was taken for a response")
	}
	if _, ok := note.RequestID(); ok {
		t.Error("a notification reported an ID")
	}
}

// A server error belongs to one request and must not abort the connection: a
// server that cannot answer a hover can still answer the next completion.
func TestResponseError(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":5,"error":{"code":-32601,"message":"unknown method"}}`
	m, err := read(t, frame(body))
	if err != nil {
		t.Fatalf("a server error broke the read: %v", err)
	}
	if m.Error == nil {
		t.Fatal("no error decoded")
	}
	if m.Error.Code != -32601 || !strings.Contains(m.Error.Error(), "unknown method") {
		t.Errorf("error = %+v", m.Error)
	}
}

// A string ID is legal JSON-RPC. raj only sends integers, so one that is not an
// integer belongs to the server and must not be misread as one of ours.
func TestStringIDIsNotMistakenForOurs(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":"server-1","method":"window/showMessageRequest"}`
	m, err := read(t, frame(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.RequestID(); ok {
		t.Error("a string ID was parsed as one of ours")
	}
}

// Arbitrary bytes must not panic the reader — a server can send anything,
// including nothing that resembles the protocol.
func FuzzReadMessage(f *testing.F) {
	f.Add(frame(`{"jsonrpc":"2.0","id":1}`))
	f.Add("Content-Length: 2\r\n\r\n{}")
	f.Add("garbage")
	f.Add("Content-Length: \r\n\r\n")

	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 64000 {
			return
		}
		m, err := ReadMessage(bufio.NewReader(strings.NewReader(s)))
		if err == nil && m == nil {
			t.Fatal("no message and no error")
		}
	})
}
