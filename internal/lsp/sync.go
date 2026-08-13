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

// Sync tracks which documents the server knows about and at what version.
//
// It is not safe for concurrent use: every caller is the event thread, which is
// also the only place buffers may be read.
type Sync struct {
	conn *Conn
	kind SyncKind
	open map[string]int // URI to the version the server last saw
	mu   sync.Mutex
}

// NewSync starts tracking against a connection.
//
// The kind comes from the server's advertised textDocumentSync. A server that
// asked for incremental updates and is sent whole documents will usually cope,
// but one that asked for none and is sent anything may not, so the advertised
// value is honoured rather than assumed.
func NewSync(conn *Conn, kind SyncKind) *Sync {
	return &Sync{conn: conn, kind: kind, open: map[string]int{}}
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
		s.open[uri] = version
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

// Change tells the server a document has new contents.
//
// A change for a document the server was never told about is dropped rather
// than sent. The server would reject it, and worse, some accept it and build a
// phantom document that answers every later request from nothing.
//
// A version that has not moved is also dropped: the buffer is unchanged, and a
// didChange claiming otherwise makes the server redo work for nothing on every
// keystroke that does not edit.
func (s *Sync) Change(path, text string, version int) error {
	if s.conn == nil {
		return ErrClosed
	}
	if s.kind == SyncNone {
		return nil
	}
	uri := URI(path)
	s.mu.Lock()
	last, known := s.open[uri]
	if known && version > last {
		s.open[uri] = version
	}
	s.mu.Unlock()
	if !known || version <= last {
		return nil
	}
	// Whole documents, even where the server accepts incremental changes.
	//
	// Incremental is what large files want, and it is the obvious next step —
	// but it requires the edit ranges, in UTF-16, for every edit since the last
	// notification, and getting one of them wrong desynchronises the server's
	// copy silently and permanently. Whole-document sync cannot desynchronise
	// by construction. The cost is bounded by the file, not the session, and
	// the change is already debounced by the caller.
	return s.conn.Notify("textDocument/didChange", map[string]any{
		"textDocument": map[string]any{"uri": uri, "version": version},
		"contentChanges": []map[string]any{
			{"text": text},
		},
	})
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
	v, ok := s.open[URI(path)]
	return v, ok
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
