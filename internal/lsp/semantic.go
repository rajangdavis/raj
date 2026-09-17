package lsp

import (
	"context"
	"encoding/json"
	"sort"
)

// Semantic tokens. A server that advertises semanticTokensProvider annotates
// the document with typed, modified runs — "this identifier is a function",
// "this name is a deprecated method" — and the client paints them. It is the
// one place where the server's own understanding of the code can refine what
// the local highlighter guessed.
//
// Two protocol facts shape everything here:
//
//   - The response never names a token type. It sends an integer index into the
//     server's own legend, which the initialize reply carries as an ordered
//     list of names. The same integer means different things against different
//     servers, so the legend is read from the server's capabilities and
//     threaded through decoding; nothing here invents an index of its own.
//   - The data is delta-encoded and counted in UTF-16 code units, the same grid
//     every other position in this package uses. A token's start is relative to
//     the previous token's start on the same line, or absolute within the line
//     when the line moved; a token's length is in UTF-16 code units and may
//     cross a line.

// StandardTokenTypes is the set of semantic token types this client can
// interpret, in the order the protocol names them. It is what the client
// announces in its capabilities. A server answers with its own legend — often a
// subset in a different order — and every type index in a response is into that
// legend, never into this list.
func StandardTokenTypes() []string {
	return []string{
		"namespace", "type", "class", "enum", "interface", "struct",
		"typeParameter", "parameter", "variable", "property", "enumMember",
		"event", "function", "method", "macro", "keyword", "modifier",
		"comment", "string", "number", "regexp", "operator", "decorator",
	}
}

// StandardTokenModifiers is the set of token modifiers this client can
// interpret, announced alongside the types. Like StandardTokenTypes it is the
// client's offer; a response's modifier bitmask indexes the server's legend.
func StandardTokenModifiers() []string {
	return []string{
		"declaration", "definition", "readonly", "static", "deprecated",
		"abstract", "async", "modification", "documentation", "defaultLibrary",
	}
}

// SemanticTokensLegend is the server's ordered lists of token type and modifier
// names. A response's type and modifier indices are into these lists, never
// into StandardTokenTypes or any other list the client keeps.
type SemanticTokensLegend struct {
	TokenTypes     []string
	TokenModifiers []string
}

// SemanticTokensLegend reads the legend out of the raw provider. ok is false
// when the server did not advertise semantic tokens, or advertised them with no
// usable legend — and a response's indices cannot be read against it without
// one. The provider is an options object here, not a boolean, but a server that
// sent the boolean false still fails Supports and lands in the same "no" case.
func (c ServerCapabilities) SemanticTokensLegend() (SemanticTokensLegend, bool) {
	if !Supports(c.SemanticTokensProvider) {
		return SemanticTokensLegend{}, false
	}
	var opts struct {
		Legend *struct {
			TokenTypes     []string `json:"tokenTypes"`
			TokenModifiers []string `json:"tokenModifiers"`
		} `json:"legend"`
	}
	if json.Unmarshal(c.SemanticTokensProvider, &opts) != nil || opts.Legend == nil || len(opts.Legend.TokenTypes) == 0 {
		return SemanticTokensLegend{}, false
	}
	return SemanticTokensLegend{
		TokenTypes:     opts.Legend.TokenTypes,
		TokenModifiers: opts.Legend.TokenModifiers,
	}, true
}

// SemanticToken is one decoded token: where it starts, how long it is, and the
// names its type and modifier indices resolved to through the server's legend.
//
// Type is empty when the index was past the end of the legend. That is a broken
// server, or a legend that changed under a live connection, and the caller must
// treat it as unknown rather than guess a colour: the honest result is to leave
// the local highlighter's colour in place.
type SemanticToken struct {
	Start     Position
	Length    int // UTF-16 code units, as the protocol counts them
	Type      string
	Modifiers []string
}

// HasModifier reports whether the token carries one of its legend's modifiers
// by name.
func (t SemanticToken) HasModifier(name string) bool {
	for _, m := range t.Modifiers {
		if m == name {
			return true
		}
	}
	return false
}

// SemanticTokens is a decoded response to a full, delta or range request. It
// carries the complete flat data array as well as the decoded tokens, because a
// later full/delta request is computed against that array: the resultId names
// the state, and the data is the array the server's edits index.
type SemanticTokens struct {
	// ResultID is the server's handle for this result, the input to a later
	// full/delta request as previousResultId. It is empty when the server sent
	// none, which leaves nothing to chain a delta from and forces a full
	// request next time.
	ResultID string
	// Data is the raw, flattened integer array the tokens were decoded from. A
	// delta reply's edits index this array, not the decoded tokens, so it is
	// kept alongside the resultId to serve as the base of the next delta.
	Data   []uint32
	Tokens []SemanticToken
}

// wireSemanticTokens is the response shape: an optional resultId and the flat
// delta-encoded data array.
type wireSemanticTokens struct {
	ResultID string   `json:"resultId"`
	Data     []uint32 `json:"data"`
}

// SemanticTokensEdit is one edit to a full response's flat data array: the
// deleteCount integers starting at start are replaced by data. The offsets
// index the flattened integer array, not decoded tokens, and they are in the
// coordinates of the array the response is based on — not of the array after
// earlier edits in the same response.
type SemanticTokensEdit struct {
	Start       int
	DeleteCount int
	Data        []uint32
}

// wireSemanticTokensEdit is the protocol's SemanticTokensEdit.
type wireSemanticTokensEdit struct {
	Start       uint32   `json:"start"`
	DeleteCount uint32   `json:"deleteCount"`
	Data        []uint32 `json:"data"`
}

// wireSemanticTokensDelta is a full/delta response. Edits is a pointer so an
// absent edits array — the protocol lets a server answer full/delta with a
// whole SemanticTokens result — is distinguishable from a present-but-empty
// one, which means the tokens are unchanged under a new resultId.
type wireSemanticTokensDelta struct {
	ResultID string                    `json:"resultId"`
	Edits    *[]wireSemanticTokensEdit `json:"edits"`
	Data     []uint32                  `json:"data"`
}

// SemanticTokensDelta is a decoded full/delta response. Exactly one of Edits
// and Full describes the change: Full is true when the server sent a complete
// data array instead of edits. A full replacement must be taken as-is rather
// than applied to the previous array, or the old tokens would be kept under a
// new resultId.
type SemanticTokensDelta struct {
	ResultID string
	Edits    []SemanticTokensEdit
	Data     []uint32
	Full     bool
}

// ApplySemanticTokensDelta returns the flat data array a delta describes,
// starting from the previous array. A full replacement ignores previous.
//
// The protocol says every edit in one response is based on the same state of
// the array and that a client must not assume they are sorted, so the edits are
// sorted and then applied from the back: an earlier edit's offsets stay valid
// while a later one lands. ok is false when an edit is out of range or two
// edits overlap, because the array the response describes is then not well
// defined. A caller that gets false must fall back to a full request: a
// partially applied array describes neither the old state nor the new one.
func ApplySemanticTokensDelta(previous []uint32, d SemanticTokensDelta) ([]uint32, bool) {
	if d.Full {
		return append([]uint32(nil), d.Data...), true
	}
	edits := append([]SemanticTokensEdit(nil), d.Edits...)
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].Start != edits[j].Start {
			return edits[i].Start < edits[j].Start
		}
		return edits[i].DeleteCount < edits[j].DeleteCount
	})
	prevEnd := -1
	for _, e := range edits {
		if e.Start < 0 || e.DeleteCount < 0 || e.Start > len(previous) || e.Start+e.DeleteCount > len(previous) {
			return nil, false
		}
		if prevEnd > e.Start {
			return nil, false
		}
		prevEnd = e.Start + e.DeleteCount
	}
	out := append([]uint32(nil), previous...)
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		next := make([]uint32, 0, len(out)-e.DeleteCount+len(e.Data))
		next = append(next, out[:e.Start]...)
		next = append(next, e.Data...)
		next = append(next, out[e.Start+e.DeleteCount:]...)
		out = next
	}
	return out, true
}

// RequestSemanticTokensFull asks for every semantic token in a document.
//
// The full request names the document and nothing else; the protocol has no
// range parameter on it. The legend must be the one from the initialize reply,
// because the response's indices are meaningless without it.
func RequestSemanticTokensFull(ctx context.Context, c *Conn, path string, legend SemanticTokensLegend) (*SemanticTokens, error) {
	if c == nil {
		return nil, ErrClosed
	}
	params := map[string]any{"textDocument": map[string]any{"uri": URI(path)}}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/semanticTokens/full", params, &raw); err != nil {
		return nil, err
	}
	return decodeSemanticTokens(raw, legend), nil
}

// RequestSemanticTokens fetches a document's semantic tokens, preferring the
// delta form when the caller holds a previous result.
//
// previous is the caller's last result — its ResultID and Data — or nil. With a
// resultId to send, the full/delta method is used and the reply's edits are
// applied to previous.Data; with none, the full method is used. A delta reply
// is used only when it advances the resultId and its edits apply cleanly: a
// server that rejects the resultId, a reply that does not parse, one with no
// resultId or one that reuses the previous resultId, and edits that cannot be
// applied to the array we hold all fall back to the full method rather than
// return an array that describes neither state. A whole-result reply carries
// its own data and is taken as-is.
//
// The returned result always carries the complete data array and the decoded
// tokens, so the caller can keep it as the base of the next request.
func RequestSemanticTokens(ctx context.Context, c *Conn, path string, legend SemanticTokensLegend, previous *SemanticTokens) (*SemanticTokens, error) {
	if previous != nil && previous.ResultID != "" {
		delta, err := RequestSemanticTokensFullDelta(ctx, c, path, previous.ResultID)
		if err == nil && delta.Full {
			// A whole result is self-contained, so it is taken as-is whether
			// or not it carries a fresh resultId.
			return &SemanticTokens{
				ResultID: delta.ResultID,
				Data:     delta.Data,
				Tokens:   DecodeSemanticTokens(delta.Data, legend),
			}, nil
		}
		if err == nil && delta.ResultID != "" && delta.ResultID != previous.ResultID {
			if data, ok := ApplySemanticTokensDelta(previous.Data, *delta); ok {
				return &SemanticTokens{
					ResultID: delta.ResultID,
					Data:     data,
					Tokens:   DecodeSemanticTokens(data, legend),
				}, nil
			}
		}
		// A result the server no longer knows (an error or malformed reply), a
		// reply with no resultId to chain from, one that reuses the previous
		// resultId instead of advancing, and edits that cannot be applied to
		// the array we hold all mean the delta cannot be trusted; the full
		// request below is the recovery.
	}
	return RequestSemanticTokensFull(ctx, c, path, legend)
}

// RequestSemanticTokensFullDelta asks for the edits that turn a previous
// full/delta result into the document's current tokens.
//
// The reply is either a SemanticTokensDelta (edits over the previous array) or
// a whole SemanticTokens result, which the protocol permits and which
// SemanticTokensDelta reports as Full. It is an error, not an empty delta, when
// the server no longer knows previousResultID; the caller falls back to a full
// request.
func RequestSemanticTokensFullDelta(ctx context.Context, c *Conn, path, previousResultID string) (*SemanticTokensDelta, error) {
	if c == nil {
		return nil, ErrClosed
	}
	params := map[string]any{
		"textDocument":     map[string]any{"uri": URI(path)},
		"previousResultId": previousResultID,
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/semanticTokens/full/delta", params, &raw); err != nil {
		return nil, err
	}
	return decodeSemanticTokensDelta(raw)
}

// RequestSemanticTokensRange asks for the semantic tokens within one range of a
// document. The response shape is the full request's: a resultId and a flat
// data array, describing only the requested range. The protocol defines no
// delta form of the range request, so a result from here must not be used as
// the base of a full/delta request.
//
// The range is in the protocol's UTF-16 position grid, like every other range
// in this package. The request is what bounds the server's answer for a large
// document; a caller that uses it must ask again whenever the range moves,
// because tokens outside it are not sent.
func RequestSemanticTokensRange(ctx context.Context, c *Conn, path string, r Range, legend SemanticTokensLegend) (*SemanticTokens, error) {
	if c == nil {
		return nil, ErrClosed
	}
	params := map[string]any{
		"textDocument": map[string]any{"uri": URI(path)},
		"range":        r,
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/semanticTokens/range", params, &raw); err != nil {
		return nil, err
	}
	return decodeSemanticTokens(raw, legend), nil
}

// decodeSemanticTokensDelta reads a full/delta response. A null result decodes
// to a full replacement with no data, which clears the tokens; the alternative
// is to keep stale tokens for a resultId the server did not send. Edits being
// absent is the protocol's full-replacement form, not an error. A response that
// does not parse is an error, so the caller can fall back to a full request
// rather than paint a replacement that was never sent.
func decodeSemanticTokensDelta(raw json.RawMessage) (*SemanticTokensDelta, error) {
	out := &SemanticTokensDelta{}
	if isNull(raw) {
		out.Full = true
		return out, nil
	}
	var wire wireSemanticTokensDelta
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}
	out.ResultID = wire.ResultID
	if wire.Edits == nil {
		out.Full = true
		out.Data = wire.Data
		return out, nil
	}
	for _, e := range *wire.Edits {
		out.Edits = append(out.Edits, SemanticTokensEdit{
			Start:       int(e.Start),
			DeleteCount: int(e.DeleteCount),
			Data:        e.Data,
		})
	}
	return out, nil
}

// decodeSemanticTokens reads a full response. A null result and an empty data
// array both mean "no tokens", the normal answer for a file the server has not
// indexed or has nothing to say about.
func decodeSemanticTokens(raw json.RawMessage, legend SemanticTokensLegend) *SemanticTokens {
	out := &SemanticTokens{}
	if isNull(raw) {
		return out
	}
	var wire wireSemanticTokens
	if json.Unmarshal(raw, &wire) != nil {
		return out
	}
	out.ResultID = wire.ResultID
	out.Data = wire.Data
	out.Tokens = DecodeSemanticTokens(wire.Data, legend)
	return out
}

// DecodeSemanticTokens expands the protocol's flat, delta-encoded data array
// into tokens.
//
// Every five integers are one token: deltaLine, deltaStartChar, length,
// tokenType, tokenModifiers. deltaLine is relative to the previous token's line
// — which is its start line even when the token spans lines — and
// deltaStartChar is relative to the previous token's start when deltaLine is
// zero, and absolute within the new line otherwise. Length is in UTF-16 code
// units.
//
// A trailing group of fewer than five integers is dropped rather than failing
// the whole answer: the data is a flat array, and a truncated tail describes a
// token that cannot exist while the complete tokens before it are real.
//
// A type index past the end of the legend yields an empty type, and a modifier
// bit past the end of the legend is ignored; neither is guessed at. The span is
// still decoded, so the caller can decide whether a token it cannot name is
// worth keeping (it is not: see the app's overlay builder).
func DecodeSemanticTokens(data []uint32, legend SemanticTokensLegend) []SemanticToken {
	out := make([]SemanticToken, 0, len(data)/5)
	line, char := 0, 0
	for i := 0; i+5 <= len(data); i += 5 {
		dLine := int(data[i])
		dChar := int(data[i+1])
		if dLine != 0 {
			line += dLine
			char = dChar
		} else {
			char += dChar
		}
		tok := SemanticToken{
			Start:  Position{Line: line, Character: char},
			Length: int(data[i+2]),
			Type:   legend.typeName(data[i+3]),
		}
		if mask := data[i+4]; mask != 0 {
			tok.Modifiers = legend.modifierNames(mask)
		}
		out = append(out, tok)
	}
	return out
}

// typeName is the legend's name at index, or "" when the index is past the end
// of the list. A broken index is not a reason to drop the token's span — the
// caller skips a token with no type when it paints, which leaves the local
// highlighter's colour rather than guessing one.
func (l SemanticTokensLegend) typeName(index uint32) string {
	if int(index) >= len(l.TokenTypes) {
		return ""
	}
	return l.TokenTypes[index]
}

// modifierNames turns a modifier bitmask into the names of the set bits. A bit
// past the end of the legend is skipped rather than widened to a name it does
// not have.
func (l SemanticTokensLegend) modifierNames(mask uint32) []string {
	var out []string
	for bit := 0; bit < 32; bit++ {
		if mask&(1<<uint(bit)) == 0 {
			continue
		}
		if bit >= len(l.TokenModifiers) {
			continue
		}
		out = append(out, l.TokenModifiers[bit])
	}
	return out
}
