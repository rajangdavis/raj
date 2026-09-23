package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/lsp"
)

// The capability is read from documentLinkProvider with the same rule and voice
// every provider uses: absent and false are "does not support document links",
// and an options object is support. Without the gate the chord asks a server
// that answers method-not-found and reports the transport error instead of the
// missing feature.
func TestDocumentLinkCapabilityGate(t *testing.T) {
	if got := documentLinkGap(lsp.ServerCapabilities{}); got != "language server does not support document links" {
		t.Errorf("absent provider: gap = %q", got)
	}
	if got := documentLinkGap(lsp.ServerCapabilities{DocumentLinkProvider: json.RawMessage(`false`)}); got != "language server does not support document links" {
		t.Errorf("false provider: gap = %q", got)
	}
	if got := documentLinkGap(lsp.ServerCapabilities{DocumentLinkProvider: json.RawMessage(`{"resolveProvider":true}`)}); got != "" {
		t.Errorf("advertised provider: gap = %q, want none", got)
	}
}

// linkAnswer is a document-link answer for the harness's file at the position
// the chord was pressed, measured on the buffer's current version — the shape
// followLink parks.
func linkAnswer(h *harness, line, col int, links ...lsp.DocumentLink) lspAnswer {
	return lspAnswer{
		gen: h.lspGen, kind: answerDocumentLink,
		path: h.docPath(h.Pane()), docVersion: int(h.Pane().File.Session().Version()),
		line: line, col: col, links: links,
	}
}

// A file: link opens its target through the same OpenFile path every other jump
// uses. The link range is what says the caret is on it; without following it
// the user gets a status line and no file.
func TestFollowLinkOpensAFileTarget(t *testing.T) {
	h := newHarness(t, "see other.go here\n")
	other := filepath.Join(h.primaryRoot(), "other.go")
	if err := os.WriteFile(other, []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.lspGen = 1
	h.park(linkAnswer(h, 0, 5, lsp.DocumentLink{
		Range:  lsp.Range{Start: lsp.Position{Line: 0, Character: 4}, End: lsp.Position{Line: 0, Character: 12}},
		Target: lsp.URI(other),
	}))
	h.applyAnswer()
	if got := h.Tabs.Active().File.Path; got != other {
		t.Errorf("active file = %q, want the link target %q", got, other)
	}
}

// A non-file link is refused out loud: a terminal editor has no browser, and
// silently doing nothing would leave the user unable to tell a missing link
// from an unopenable one. The status names the scheme.
func TestFollowLinkRefusesANonFileTarget(t *testing.T) {
	h := newHarness(t, "see https://example.com here\n")
	before := h.Tabs.Active().File.Path
	h.lspGen = 1
	h.park(linkAnswer(h, 0, 8, lsp.DocumentLink{
		Range:  lsp.Range{Start: lsp.Position{Line: 0, Character: 4}, End: lsp.Position{Line: 0, Character: 23}},
		Target: "https://example.com",
	}))
	h.applyAnswer()
	if got := h.Tabs.Active().File.Path; got != before {
		t.Errorf("a non-file link opened %q", got)
	}
	if s := h.Status(); !strings.Contains(s, "cannot open") || !strings.Contains(s, "https") {
		t.Errorf("status = %q, want a refusal naming the scheme", s)
	}
}

// A link the server left unresolved is refused by name: the target is missing,
// not the file, and saying which is what keeps the message actionable.
func TestFollowLinkRefusesAnUnresolvedLink(t *testing.T) {
	h := newHarness(t, "see other.go here\n")
	before := h.Tabs.Active().File.Path
	h.lspGen = 1
	h.park(linkAnswer(h, 0, 5, lsp.DocumentLink{
		Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 4}, End: lsp.Position{Line: 0, Character: 12}},
		Data:  json.RawMessage(`{"token":"abc"}`),
	}))
	h.applyAnswer()
	if got := h.Tabs.Active().File.Path; got != before {
		t.Errorf("an unresolved link opened %q", got)
	}
	if s := h.Status(); !strings.Contains(s, "no target") {
		t.Errorf("status = %q, want the unresolved-link refusal", s)
	}
}

// A link answer measured on an older version is dropped: its ranges are offsets
// into text that is gone, so following it would open a path the user never
// pointed at. Without the version check the stale answer is followed anyway.
func TestFollowLinkDropsAStaleAnswer(t *testing.T) {
	h := newHarness(t, "see other.go here\n")
	before := h.Tabs.Active().File.Path
	h.lspGen = 1
	ans := linkAnswer(h, 0, 5, lsp.DocumentLink{
		Range:  lsp.Range{Start: lsp.Position{Line: 0, Character: 4}, End: lsp.Position{Line: 0, Character: 12}},
		Target: "file:///nowhere.go",
	})
	ans.docVersion--
	h.park(ans)
	h.applyAnswer()
	if got := h.Tabs.Active().File.Path; got != before {
		t.Errorf("a stale link answer opened %q", got)
	}
	if s := h.Status(); !strings.Contains(s, "changed") {
		t.Errorf("status = %q, want the stale answer named", s)
	}
}

// A caret that is not on any link says so, rather than following the nearest
// one or opening nothing silently.
func TestFollowLinkWithNoLinkAtTheCaret(t *testing.T) {
	h := newHarness(t, "see other.go here\n")
	h.lspGen = 1
	h.park(linkAnswer(h, 0, 0)) // no links
	h.applyAnswer()
	if s := h.Status(); !strings.Contains(s, "no link at the caret") {
		t.Errorf("status = %q, want the no-link word", s)
	}
}
