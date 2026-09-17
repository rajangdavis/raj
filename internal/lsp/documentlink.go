package lsp

import (
	"context"
	"encoding/json"
	"strings"
)

// Document links. A server answers textDocument/documentLink with the spans of
// one document that name another resource — a path in a comment, a URL in a doc
// line — and the URI to open for each. A link is a range plus a target; a
// server that answers lazily leaves the target out and expects a
// documentLink/resolve round trip with the link's data to fill it in.
//
// This client decodes the full 3.17 shape (range, target, tooltip, data). It
// keeps a link without a target rather than dropping it, so the follow path can
// name what it is, and the app resolves a data-carrying link once when the
// server advertised resolveProvider — the same bounded fan-out code lenses use.

// DocumentLink is one link the server reported: the span it covers, the URI it
// opens, and the optional tooltip the server wants shown for it. Data is the
// server's opaque token for a later documentLink/resolve, kept raw so it goes
// back byte for byte.
type DocumentLink struct {
	Range   Range
	Target  string
	Tooltip string
	Data    json.RawMessage
}

// NeedsResolve reports whether the link arrived without a target and so cannot
// be followed until documentLink/resolve fills one in. A link with neither a
// target nor data is also reported as needing resolve, so a caller refuses it
// by name rather than treating it as an empty link it can open.
func (l DocumentLink) NeedsResolve() bool { return l.Target == "" }

// wireDocumentLink is the 3.17 wire shape. target is optional — its absence is
// the resolve case — and data is opaque.
type wireDocumentLink struct {
	Range   Range           `json:"range"`
	Target  string          `json:"target"`
	Tooltip string          `json:"tooltip"`
	Data    json.RawMessage `json:"data"`
}

// DocumentLinkResolve reports whether the server advertised the resolve half of
// document links. The provider is an options object or a boolean, so it stays
// raw and only the one option a caller needs is read here; a server that
// advertised a bare true asked for no resolves.
func (c ServerCapabilities) DocumentLinkResolve() bool {
	if !Supports(c.DocumentLinkProvider) {
		return false
	}
	var opts struct {
		ResolveProvider bool `json:"resolveProvider"`
	}
	if json.Unmarshal(c.DocumentLinkProvider, &opts) != nil {
		return false
	}
	return opts.ResolveProvider
}

// RequestDocumentLinks asks for every link in a document.
//
// The request names the document and nothing else: the protocol has no range
// parameter, so the server answers for the whole file and the client decides
// which link the caret is on. The result is an array or null; both empty forms
// mean "no links", which is the normal answer for most documents.
func RequestDocumentLinks(ctx context.Context, c *Conn, path string) ([]DocumentLink, error) {
	if c == nil {
		return nil, ErrClosed
	}
	params := map[string]any{"textDocument": map[string]any{"uri": URI(path)}}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/documentLink", params, &raw); err != nil {
		return nil, err
	}
	return decodeDocumentLinks(raw), nil
}

// ResolveDocumentLink fills in a link's target through documentLink/resolve.
//
// The link is sent back with its data, which is the server's token for it; a
// server that answers with a target makes the link followable. A reply that
// carries no target is returned as-is rather than turned into an error, so the
// caller refuses it by name; a malformed reply leaves the input link unchanged,
// because a failed resolve is not a reason to forget what the request was
// about.
func ResolveDocumentLink(ctx context.Context, c *Conn, link DocumentLink) (DocumentLink, error) {
	if c == nil {
		return DocumentLink{}, ErrClosed
	}
	params := map[string]any{"range": link.Range}
	if len(link.Data) > 0 {
		params["data"] = link.Data
	}
	if link.Target != "" {
		params["target"] = link.Target
	}
	if link.Tooltip != "" {
		params["tooltip"] = link.Tooltip
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "documentLink/resolve", params, &raw); err != nil {
		return DocumentLink{}, err
	}
	out, ok := decodeDocumentLink(raw)
	if !ok {
		return link, nil
	}
	return out, nil
}

// decodeDocumentLinks reads the array, or null. A malformed entry is dropped
// rather than failing the whole answer, the same permissiveness the code-lens
// decoder applies: one bad entry must not hide the links beside it.
func decodeDocumentLinks(raw json.RawMessage) []DocumentLink {
	if isNull(raw) {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := make([]DocumentLink, 0, len(items))
	for _, it := range items {
		if link, ok := decodeDocumentLink(it); ok {
			out = append(out, link)
		}
	}
	return out
}

// decodeDocumentLink reads one wire link. A link without a target is kept: it
// is the resolve case, and dropping it would silently hide the link the server
// offered.
func decodeDocumentLink(raw json.RawMessage) (DocumentLink, bool) {
	if isNull(raw) {
		return DocumentLink{}, false
	}
	var w wireDocumentLink
	if json.Unmarshal(raw, &w) != nil {
		return DocumentLink{}, false
	}
	return DocumentLink{Range: w.Range, Target: w.Target, Tooltip: w.Tooltip, Data: w.Data}, true
}

// LinkAt is the link under a position: the first link whose half-open range
// contains pos. The protocol's range end is exclusive, so a caret exactly at
// the end of one link is at the start of what follows rather than in both. It
// is a pure rule so the follow path's selection is testable without a server,
// and so an adjacent pair of links resolves deterministically.
func LinkAt(links []DocumentLink, pos Position) (DocumentLink, bool) {
	for _, l := range links {
		if !positionLess(pos, l.Range.Start) && positionLess(pos, l.Range.End) {
			return l, true
		}
	}
	return DocumentLink{}, false
}

// LinkScheme is the URI scheme of a link target: "file", "https", "mailto", or
// "" when there is no scheme at all. The caller uses it to decide whether the
// target is a file the editor can open or something outside it, and to name the
// scheme in the refusal.
func LinkScheme(target string) string {
	if i := strings.Index(target, "://"); i > 0 {
		return target[:i]
	}
	// A scheme may legally omit the "//" — mailto:, urn:. A single-letter stem
	// before the colon is a Windows drive path rather than a scheme, and a link
	// target is a URI, so it is left as no scheme rather than guessed at.
	if j := strings.IndexByte(target, ':'); j > 1 {
		return target[:j]
	}
	return ""
}
