package lsp

import (
	"encoding/json"
	"sync"
)

// Document synchronisation: telling the server what the buffers contain.
//
// Every request is answered against the server's copy of a document, so a
// desynchronised copy does not produce an error — it produces a confidently
// wrong answer at a position that no longer means anything. That failure is
// silent and looks like the server being bad, which is why the tracking here is
// stricter than the protocol requires: a document is opened exactly once,
// changes are refused for a document that was never opened, and the version on
// every change is the buffer's own.
//
// A server that advertises incremental sync is sent only the changed ranges.
// That is safe here for one reason: the tracker pins the exact text the server
// was last told about, replays the recorded edits onto a copy of it, and only
// sends ranges when the replay reproduces the buffer byte for byte. Any doubt
// — history unavailable, an edit that does not fit, a replay that disagrees —
// sends the whole document instead, which cannot desynchronise by construction.

// SyncKind is how a server wants to be told about changes, as advertised in its
// capabilities.
type SyncKind int

const (
	// SyncNone means the server does not want change notifications at all.
	SyncNone SyncKind = 0
	// SyncFull sends the whole document on every change.
	SyncFull SyncKind = 1
	// SyncIncremental sends only the ranges that changed.
	SyncIncremental SyncKind = 2
)

// Edit is one buffer change: replace the bytes in [Start, End) with Text.
//
// The coordinates are byte offsets in the frame the previous edits in the
// batch produced, which is both how the journal records an op (OpsSince hands
// them out) and how the server applies a content change, so a batch converts
// one edit at a time, in order.
type Edit struct {
	Start, End int
	Text       string
}

// tracked is what the server was last told about one document: the version,
// and the full text of its copy. The text is the pin incremental sync checks
// its work against — a range computed against anything else would move the
// server's copy to somewhere the buffer has never been.
type tracked struct {
	version int
	text    string
}

// Sync tracks which documents the server knows about and at what version.
//
// It is not safe for concurrent use: every caller is the event thread, which is
// also the only place buffers may be read.
type Sync struct {
	conn *Conn
	kind SyncKind
	open map[string]tracked
	mu   sync.Mutex
}

// NewSync starts tracking against a connection.
//
// The kind comes from the server's advertised textDocumentSync. A server that
// asked for incremental updates and is sent whole documents will usually cope,
// but one that asked for none and is sent anything may not, so the advertised
// value is honoured rather than assumed.
func NewSync(conn *Conn, kind SyncKind) *Sync {
	return &Sync{conn: conn, kind: kind, open: map[string]tracked{}}
}

// Open tells the server about a document. Opening one that is already open is a
// no-op rather than a second didOpen: servers treat a duplicate open as a
// protocol error, and the editor has several paths that can reach the same file.
func (s *Sync) Open(path, languageID, text string, version int) error {
	if s.conn == nil {
		return ErrClosed
	}
	uri := URI(path)
	s.mu.Lock()
	_, already := s.open[uri]
	if !already {
		s.open[uri] = tracked{version: version, text: text}
	}
	s.mu.Unlock()
	if already {
		return nil
	}
	return s.conn.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": languageID,
			"version":    version,
			"text":       text,
		},
	})
}

// Change tells the server a document has new contents. edits are the buffer's
// changes since the version the server last saw, in application order, and may
// be nil when that history is unavailable — nil never disables sync, it just
// costs the ranges.
//
// A change for a document the server was never told about is dropped rather
// than sent. The server would reject it, and worse, some accept it and build a
// phantom document that answers every later request from nothing.
//
// A change is sent when the version moved OR the text no longer matches what
// the server holds. The second condition is not redundant: a version only
// counts journal entries, and a session replaced by a reload starts counting
// from zero again, so an old version — even a smaller or equal one — can sit
// on new text. Dropping it would leave the server's copy frozen at whatever
// it held before the reload.
func (s *Sync) Change(path, text string, version int, edits []Edit) error {
	if s.conn == nil {
		return ErrClosed
	}
	if s.kind == SyncNone {
		return nil
	}
	uri := URI(path)
	s.mu.Lock()
	last, known := s.open[uri]
	moved := known && (version != last.version || text != last.text)
	if moved {
		s.open[uri] = tracked{version: version, text: text}
	}
	s.mu.Unlock()
	if !moved {
		return nil
	}
	changes := []map[string]any{{"text": text}}
	if s.kind == SyncIncremental && version > last.version {
		if ranged, ok := incrementalChanges(last.text, edits, text); ok {
			if len(ranged) == 0 {
				return nil // the version moved; the text did not
			}
			changes = ranged
		}
	}
	return s.conn.Notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": version},
		"contentChanges": changes,
	})
}

// maxIncrementalEdits bounds the batch converted to ranges. The conversion
// replays every edit over the pinned text, so its cost is edits × document;
// past the cap the whole document is the cheaper message as well as the
// simpler one. Typing and single actions produce a handful of edits between
// notifications; the cap is for the day an agent lands a thousand-hunk diff.
const maxIncrementalEdits = 64

// incrementalChanges converts a batch of edits to ranged content changes, each
// range in UTF-16 against the document its predecessors produced — exactly the
// document the server applies them to.
//
// ok is false, and the caller sends the whole document instead, whenever the
// batch cannot be proven right: the history is missing (nil edits), an edit
// does not fit the text it claims to describe, the batch is over the cap, or
// the replayed result disagrees with the buffer by a single byte. The check is
// the feature: a range is only ever sent after the bytes it produces have been
// seen to be the bytes the buffer holds, so incremental sync cannot
// desynchronise by construction either.
func incrementalChanges(old string, edits []Edit, new string) ([]map[string]any, bool) {
	if edits == nil || len(edits) > maxIncrementalEdits {
		return nil, false
	}
	working := old
	var changes []map[string]any
	for _, e := range edits {
		if e.Start == e.End && e.Text == "" {
			continue // a journal op that changed nothing is not a change
		}
		if e.Start < 0 || e.End < e.Start || e.End > len(working) {
			return nil, false
		}
		d := NewDocument(working)
		r := Range{Start: d.Position(e.Start), End: d.Position(e.End)}
		changes = append(changes, map[string]any{"range": r, "text": e.Text})
		// Replay the change the way the server applies it: the range converted
		// back to bytes. A mid-rune edge would convert back to a rune boundary
		// and splice different bytes than the edit named — the final check
		// refuses the batch for it, and the whole document goes out instead.
		lo, hi := d.Span(r)
		working = working[:lo] + e.Text + working[hi:]
	}
	if working != new {
		return nil, false
	}
	return changes, true
}

// Save tells the server a document was written, which some servers use to run
// slower checks they skip while typing.
func (s *Sync) Save(path, text string) error {
	if s.conn == nil {
		return ErrClosed
	}
	uri := URI(path)
	s.mu.Lock()
	_, known := s.open[uri]
	s.mu.Unlock()
	if !known {
		return nil
	}
	return s.conn.Notify("textDocument/didSave", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"text":         text,
	})
}

// Close tells the server to forget a document, which also tells it to drop the
// diagnostics it published for one — otherwise a closed file's problems stay on
// screen with nothing to clear them.
func (s *Sync) Close(path string) error {
	if s.conn == nil {
		return ErrClosed
	}
	uri := URI(path)
	s.mu.Lock()
	_, known := s.open[uri]
	delete(s.open, uri)
	s.mu.Unlock()
	if !known {
		return nil
	}
	return s.conn.Notify("textDocument/didClose", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
}

// IsOpen reports whether the server has been told about a document. Callers use
// it to avoid sending a request against a document the server does not have,
// which would be answered from nothing.
func (s *Sync) IsOpen(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.open[URI(path)]
	return ok
}

// Version is the version the server last saw for a document, and whether it
// knows the document at all.
func (s *Sync) Version(path string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.open[URI(path)]
	return t.version, ok
}

// Count is how many documents the server is tracking.
func (s *Sync) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.open)
}

// SyncKindOf reads the advertised textDocumentSync, which the protocol allows
// to be either a bare number or an options object.
//
// An unrecognised or absent value becomes SyncFull rather than SyncNone,
// because a server that does not say is far more likely to be an older one that
// assumes full sync than one that wants no updates at all — and guessing None
// leaves every feature answering from a document frozen at open.
func SyncKindOf(raw []byte) SyncKind {
	if len(raw) == 0 || string(raw) == "null" {
		return SyncFull
	}
	// `null` unmarshals into an int without error and leaves it zero, which
	// would silently read as SyncNone — the one value that freezes every
	// document at the moment it was opened. It is excluded above rather than
	// relied on to fail.
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		switch SyncKind(n) {
		case SyncNone, SyncFull, SyncIncremental:
			return SyncKind(n)
		}
		return SyncFull
	}
	var obj struct {
		Change *int `json:"change"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Change != nil {
		switch SyncKind(*obj.Change) {
		case SyncNone, SyncFull, SyncIncremental:
			return SyncKind(*obj.Change)
		}
	}
	return SyncFull
}

// LanguageID maps a file extension to the identifier servers expect. An unknown
// extension yields an empty string, which callers treat as "do not open this
// file": a server told about a language it does not handle may answer requests
// about it badly rather than not at all.
func LanguageID(path string) string {
	ext := ""
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			ext = path[i:]
			break
		}
		if path[i] == '/' || path[i] == '\\' {
			break
		}
	}
	switch ext {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".py":
		return "python"
	case ".rb":
		return "ruby"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".hpp", ".hh":
		return "cpp"
	case ".java":
		return "java"
	case ".json":
		return "json"
	case ".md", ".markdown":
		return "markdown"
	case ".sh", ".bash":
		return "shellscript"
	case ".css":
		return "css"
	case ".html":
		return "html"
	case ".yml", ".yaml":
		return "yaml"
	case ".toml":
		return "toml"
	}
	return ""
}
