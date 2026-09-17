package app

import (
	"encoding/json"
	"testing"

	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/syntax"
)

// stok is a terse fixture: one semantic token at a position.
func stok(line, char, length int, typ string) lsp.SemanticToken {
	return lsp.SemanticToken{
		Start:  lsp.Position{Line: line, Character: char},
		Length: length,
		Type:   typ,
	}
}

// installSemantic parks and applies a semantic answer as a server would, so a
// test can get an overlay onto the active pane without a real server process.
func (h *harness) installSemantic(tokens ...lsp.SemanticToken) {
	h.semanticGen++
	h.park(lspAnswer{
		gen:             h.semanticGen,
		kind:            answerSemantic,
		path:            h.docPath(h.Pane()),
		semanticVersion: int(h.Pane().File.Session().Version()),
		semTokens:       tokens,
	})
	h.applyAnswer()
}

// The store keeps the overlay with the version it describes, which is what lets
// the install and invalidate paths drop an answer for text the buffer has left.
func TestSemanticStoreRoundTrips(t *testing.T) {
	s := newSemanticStore()
	set := editor.NewSemanticSet(map[int][]syntax.Span{1: {{Start: 2, End: 4}}})
	s.set("/w/a.go", 7, set)
	got, version, ok := s.forPath("/w/a.go")
	if !ok || version != 7 || got != set {
		t.Fatalf("forPath = %+v version %d ok %v, want the stored set", got, version, ok)
	}
	if _, _, ok := s.forPath("/w/b.go"); ok {
		t.Error("an untouched path reported an answer")
	}
	s.clear("/w/a.go")
	if _, _, ok := s.forPath("/w/a.go"); ok {
		t.Error("a cleared path still reports an answer")
	}
}

// An answer with no tokens is stored, not dropped: the version still says which
// text it describes, so "no tokens" is distinct from "never asked", and the
// overlay that was there must come off.
func TestSemanticStoreKeepsEmptyAnswer(t *testing.T) {
	s := newSemanticStore()
	s.set("/w/a.go", 3, nil)
	got, version, ok := s.forPath("/w/a.go")
	if !ok || version != 3 || got != nil {
		t.Fatalf("empty answer = %+v version %d ok %v", got, version, ok)
	}
}

// A semantic answer installs an overlay the renderer reads. Without the install
// the decoded tokens never reach a pane and the file keeps only its chroma
// colour — the feature would decode perfectly and paint nothing.
func TestSemanticOverlayInstalls(t *testing.T) {
	h := newHarness(t, "hello world\n")
	h.installSemantic(stok(0, 6, 5, "function"))
	if h.Pane().File.Semantic == nil {
		t.Fatal("no overlay was installed")
	}
	spans := h.Pane().File.Semantic.At(0)
	if len(spans) != 1 {
		t.Fatalf("line 0 overlay = %+v, want one span", spans)
	}
	want, ok := syntax.SemanticStyle("function")
	if !ok {
		t.Fatal("setup: function has no role")
	}
	if spans[0] != (syntax.Span{Start: 6, End: 11, Style: want}) {
		t.Errorf("span = %+v, want 6..11 in the function role", spans[0])
	}
}

// A type the palette has no role for, or an out-of-range legend index (which
// decodes to the empty type), paints nothing: the byte keeps whatever chroma
// gave it. Guessing a colour would be indistinguishable from a correct one.
func TestSemanticOverlaySkipsUnknownType(t *testing.T) {
	h := newHarness(t, "hello world\n")
	h.installSemantic(stok(0, 0, 5, ""), stok(0, 6, 5, "not-a-real-token-type"))
	if h.Pane().File.Semantic != nil {
		if spans := h.Pane().File.Semantic.At(0); len(spans) != 0 {
			t.Errorf("unknown types painted %+v; want nothing", spans)
		}
	}
}

// An answer for a version the buffer has left is dropped rather than painted
// onto moved bytes: a stale span is not merely the wrong colour, it is the
// wrong colour in the wrong place.
func TestStaleSemanticAnswerIsDropped(t *testing.T) {
	h := newHarness(t, "hello world\n")
	h.semanticGen++
	h.park(lspAnswer{
		gen:             h.semanticGen,
		kind:            answerSemantic,
		path:            h.docPath(h.Pane()),
		semanticVersion: int(h.Pane().File.Session().Version()) + 1,
		semTokens:       []lsp.SemanticToken{stok(0, 6, 5, "function")},
	})
	h.applyAnswer()
	if h.Pane().File.Semantic != nil && len(h.Pane().File.Semantic.At(0)) != 0 {
		t.Errorf("a stale answer was installed: %+v", h.Pane().File.Semantic.At(0))
	}
}

// An edit moves the bytes the overlay was measured on, so the next frame drops
// it before it can paint a token at the wrong offset. This is the version-memo
// drop: without it the stale colours would survive until the next server answer.
func TestSemanticOverlayDroppedWhenTextMoves(t *testing.T) {
	h := newHarness(t, "hello world\n")
	h.Draw()
	h.installSemantic(stok(0, 6, 5, "function"))
	if h.Pane().File.Semantic == nil {
		t.Fatal("setup: the overlay was not installed")
	}
	h.typeText("x")
	h.Draw()
	if h.Pane().File.Semantic != nil && len(h.Pane().File.Semantic.At(0)) != 0 {
		t.Errorf("an overlay survived the edit: %+v", h.Pane().File.Semantic.At(0))
	}
}

// The guard keeps the idle tick from asking the server the same question every
// 150 ms: only a moved path or version makes a new request worth making.
func TestSemanticWantedGuard(t *testing.T) {
	a := &App{}
	if !a.semanticWanted("/w/a.go", 1) {
		t.Fatal("a fresh app did not want tokens")
	}
	a.semanticReq = semanticReq{path: "/w/a.go", version: 1}
	if a.semanticWanted("/w/a.go", 1) {
		t.Error("the same path and version were requested again")
	}
	if !a.semanticWanted("/w/a.go", 2) {
		t.Error("a moved version did not re-request")
	}
	if !a.semanticWanted("/w/b.go", 1) {
		t.Error("a different path did not re-request")
	}
}

// The conversion counts UTF-16 code units, not bytes: a token after a two-byte
// rune starts at the right byte. Without this every non-ASCII file would have
// its semantic colours shifted by the number of multi-byte runes before them.
func TestSemanticSpansUsesUTF16AndSkipsUnknown(t *testing.T) {
	doc := lsp.NewDocument("héllo wörld\n")
	spans := semanticSpans(doc, []lsp.SemanticToken{
		stok(0, 6, 5, "function"),
		stok(0, 0, 1, ""),
	})
	got := spans[0]
	if len(got) != 1 {
		t.Fatalf("line 0 spans = %+v, want only the named token", got)
	}
	want, _ := syntax.SemanticStyle("function")
	if got[0] != (syntax.Span{Start: 7, End: 13, Style: want}) {
		t.Errorf("span = %+v, want bytes 7..13 in the function role", got[0])
	}
}

// The gate reads SemanticTokensProvider in the shared "does not support" voice,
// and a present-and-false provider means no. Without this read the feature would
// ask a method the server answers method-not-found.
func TestSemanticCapabilityGate(t *testing.T) {
	const want = "language server does not support semantic tokens"
	if got := semanticGap(lsp.ServerCapabilities{}); got != want {
		t.Errorf("absent provider: %q", got)
	}
	if got := semanticGap(lsp.ServerCapabilities{SemanticTokensProvider: json.RawMessage(`false`)}); got != want {
		t.Errorf("false provider: %q", got)
	}
	advertised := lsp.ServerCapabilities{SemanticTokensProvider: json.RawMessage(
		`{"legend":{"tokenTypes":["keyword"],"tokenModifiers":[]}}`)}
	if got := semanticGap(advertised); got != "" {
		t.Errorf("advertised provider: %q, want none", got)
	}
}

// Client support is announced or a server never sends a response: the token
// types and modifiers it may index, the one coordinate encoding raj reads, and
// the requests it can answer. The full request and its delta form are claimed,
// so the server may answer a full request with edits against the previous
// resultId. The range form is implemented in the lsp package but not yet sent
// from the draw path, so it is not claimed and a server does not send a shape
// this client would discard.
func TestClientCapabilitiesAdvertiseSemanticTokens(t *testing.T) {
	caps := clientCapabilities()
	td, _ := caps["textDocument"].(map[string]any)
	if td == nil {
		t.Fatal("no textDocument capabilities")
	}
	st, ok := td["semanticTokens"].(map[string]any)
	if !ok {
		t.Fatalf("semanticTokens = %T, want an options object", td["semanticTokens"])
	}
	types, ok := st["tokenTypes"].([]string)
	if !ok || len(types) == 0 || types[0] != "namespace" {
		t.Errorf("tokenTypes = %v, want the standard list", st["tokenTypes"])
	}
	if mods, ok := st["tokenModifiers"].([]string); !ok || len(mods) == 0 {
		t.Errorf("tokenModifiers = %v, want the standard list", st["tokenModifiers"])
	}
	if formats, ok := st["formats"].([]string); !ok || len(formats) != 1 || formats[0] != "relative" {
		t.Errorf("formats = %v, want [relative]", st["formats"])
	}
	req, ok := st["requests"].(map[string]any)
	if !ok {
		t.Fatalf("requests = %T, want an options object", st["requests"])
	}
	if req["range"] != false {
		t.Errorf("range = %v, want false until range is sent from the draw path", req["range"])
	}
	if full, ok := req["full"].(map[string]any); !ok || full["delta"] != true {
		t.Errorf("full = %v, want {delta: true}", req["full"])
	}
}

// The store keeps the server's last result alongside the overlay version, so a
// later request can diff against it. Without the base the delta path has
// nothing to apply edits to and would have to fall back to a full request every
// time, which is the behavior the base exists to avoid.
func TestSemanticStoreBaseRoundTrips(t *testing.T) {
	s := newSemanticStore()
	s.note("/w/a.go", "r1", []uint32{0, 0, 1, 0, 0})
	id, data, ok := s.base("/w/a.go")
	if !ok || id != "r1" || len(data) != 5 {
		t.Fatalf("base = %q %v ok %v, want r1 and the data", id, data, ok)
	}
	// An overlay install must not disturb the base: the base belongs to the
	// reply, the overlay to the paint, and a later delta needs the former even
	// when the latter is dropped for a moved version.
	set := editor.NewSemanticSet(map[int][]syntax.Span{0: {{Start: 0, End: 1}}})
	s.set("/w/a.go", 3, set)
	id, data, ok = s.base("/w/a.go")
	if !ok || id != "r1" || len(data) != 5 {
		t.Errorf("set disturbed the base: %q %v ok %v", id, data, ok)
	}
	s.clear("/w/a.go")
	if _, _, ok := s.base("/w/a.go"); ok {
		t.Error("a cleared path still reports a base")
	}
}

// A refresh marks the server, not the document: the marker is consumed once and
// the next idle tick re-requests. Leaving it set would re-request every tick;
// never setting it would ignore the server saying its results are stale.
func TestSemanticRefreshMarker(t *testing.T) {
	ls := &langServer{}
	if ls.takeSemanticRefresh() {
		t.Fatal("an unmarked server reported a refresh")
	}
	ls.markSemanticRefresh()
	if !ls.takeSemanticRefresh() {
		t.Fatal("a marked server did not report a refresh")
	}
	if ls.takeSemanticRefresh() {
		t.Error("the marker survived its consumption")
	}
	if (&langServer{}).takeSemanticRefresh() {
		t.Error("a refresh leaked to another server")
	}
}

// A range answer rides the same version pin as a full one. The request that
// produced it does not change the pin: an answer measured on text the buffer
// has left is dropped before it can paint, because a stale span is not merely
// the wrong colour, it is the wrong range.
func TestStaleSemanticRangeAnswerIsDropped(t *testing.T) {
	h := newHarness(t, "hello world\n")
	h.semanticGen++
	h.park(lspAnswer{
		gen:             h.semanticGen,
		kind:            answerSemantic,
		path:            h.docPath(h.Pane()),
		semanticVersion: int(h.Pane().File.Session().Version()) + 1,
		semTokens:       []lsp.SemanticToken{stok(0, 6, 5, "function")},
	})
	h.applyAnswer()
	if h.Pane().File.Semantic != nil && len(h.Pane().File.Semantic.At(0)) != 0 {
		t.Errorf("a stale range answer was installed: %+v", h.Pane().File.Semantic.At(0))
	}
}
