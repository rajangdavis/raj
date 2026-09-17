package lsp

import (
	"context"
	"encoding/json"
	"strings"
)

// Signature help. Where hover describes the symbol a position sits on, this
// describes the call that position is inside: the signatures the server could
// match, which one is active, and which parameter of it the cursor is in.

// SignatureHelp is a server's answer: the candidate signatures, and the
// indices of the active one and the active parameter within it.
type SignatureHelp struct {
	Signatures      []Signature
	ActiveSignature int
	ActiveParameter int
}

// Signature is one candidate call signature. Label is the whole signature text
// the server wants shown; Documentation is flattened the same way hover
// contents are. Parameters carry the label text and, when the server sent
// offsets, the span within Label the parameter occupies — the only reliable way
// to know which part of the label to mark.
type Signature struct {
	Label         string
	Documentation string
	Parameters    []Parameter
	// ActiveParameter is the server's per-signature override, or nil when it
	// sent none. Some servers set only the top-level one, so the two are kept
	// apart rather than collapsed at decode time; Active resolves them.
	ActiveParameter *int
}

// Parameter is one parameter of a signature.
type Parameter struct {
	Label string
	// Start and End are offsets into Signature.Label in the protocol's UTF-16
	// units, and are valid only when HasOffsets is true. A parameter whose
	// label arrived as a plain string has no offsets: it is a display form and
	// need not appear verbatim in the signature, so it cannot be located
	// reliably.
	Start, End int
	HasOffsets bool
}

// signatureHelpResponse is the wire shape. documentation has the same legal
// forms as hover contents (a string or a MarkupContent), so it is kept raw and
// flattened with the same code; a parameter label is either a string or a
// [start,end] pair and is decoded per parameter.
type signatureHelpResponse struct {
	Signatures      []signatureInformation `json:"signatures"`
	ActiveSignature int                    `json:"activeSignature"`
	ActiveParameter int                    `json:"activeParameter"`
}

type signatureInformation struct {
	Label           string                 `json:"label"`
	Documentation   json.RawMessage        `json:"documentation"`
	Parameters      []parameterInformation `json:"parameters"`
	ActiveParameter *int                   `json:"activeParameter"`
}

type parameterInformation struct {
	Label json.RawMessage `json:"label"`
}

// RequestSignatureHelp asks what call the position is inside.
//
// The params are exactly the textDocument and position hover sends; no context
// object is sent. The protocol defines an absent context as an explicit
// invocation (triggerKind Invoked), which is what a chord is, and it is the
// only kind this client produces — there are no trigger characters wired up and
// no activeSignatureHelp state to re-trigger from, so a context carrying only
// those defaults would say nothing the absence does not. A nil result with no
// error is the normal answer for a position that is not inside a call.
func RequestSignatureHelp(ctx context.Context, c *Conn, path string, p Position) (*SignatureHelp, error) {
	if c == nil {
		return nil, ErrClosed
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/signatureHelp", positionParams(path, p), &raw); err != nil {
		return nil, err
	}
	if isNull(raw) {
		return nil, nil
	}
	var res signatureHelpResponse
	if json.Unmarshal(raw, &res) != nil || len(res.Signatures) == 0 {
		return nil, nil
	}
	out := &SignatureHelp{
		Signatures:      make([]Signature, 0, len(res.Signatures)),
		ActiveSignature: res.ActiveSignature,
		ActiveParameter: res.ActiveParameter,
	}
	for _, s := range res.Signatures {
		sig := Signature{
			Label:           s.Label,
			Documentation:   hoverText(s.Documentation),
			ActiveParameter: s.ActiveParameter,
		}
		for _, prm := range s.Parameters {
			sig.Parameters = append(sig.Parameters, decodeParameter(prm.Label))
		}
		out.Signatures = append(out.Signatures, sig)
	}
	return out, nil
}

// decodeParameter reads a parameter label, which is a plain string or a
// [start,end] pair of offsets into the signature label. An offset pair needs no
// matching text: it is already the span to mark.
func decodeParameter(raw json.RawMessage) Parameter {
	if isNull(raw) {
		return Parameter{}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return Parameter{Label: s}
	}
	var pair []int
	if json.Unmarshal(raw, &pair) == nil && len(pair) == 2 && pair[0] >= 0 && pair[1] >= pair[0] {
		return Parameter{Start: pair[0], End: pair[1], HasOffsets: true}
	}
	return Parameter{}
}

// Active returns the signature and parameter index the server says are active,
// clamped into range. ok is false when there is nothing to show at all. A
// server that names no active signature means the first; one that names a
// parameter outside its signature means none, so the signature is shown whole
// rather than with the last parameter wrongly marked.
func (h *SignatureHelp) Active() (Signature, int, bool) {
	if h == nil || len(h.Signatures) == 0 {
		return Signature{}, 0, false
	}
	i := h.ActiveSignature
	if i < 0 || i >= len(h.Signatures) {
		i = 0
	}
	sig := h.Signatures[i]
	p := h.ActiveParameter
	if sig.ActiveParameter != nil {
		p = *sig.ActiveParameter
	}
	if p < 0 || p >= len(sig.Parameters) {
		p = -1
	}
	return sig, p, true
}

// Text renders the active signature for the hover panel: the label, with the
// active parameter marked when the server sent offsets for it, then the
// signature's documentation.
//
// The mark is an inline-code span rather than markdown bold on purpose. The
// panel's bold rule only recognises a marker pair that flanks word bytes, and
// parameters routinely begin with `...`, `[]`, `*` or `(`, so a bold marker
// would be left on screen as literal asterisks for exactly the parameters that
// most need marking. Inline code has no such rule. A parameter whose label
// arrived as a plain string is left unmarked rather than guessed at: the string
// is a display form that need not appear verbatim in the signature, and
// emphasising the wrong occurrence of a repeated parameter is worse than
// emphasising none.
func (h *SignatureHelp) Text() string {
	sig, active, ok := h.Active()
	if !ok {
		return ""
	}
	label := sig.Label
	if active >= 0 {
		label = markParameter(label, sig.Parameters[active])
	}
	doc := strings.TrimSpace(sig.Documentation)
	if doc == "" {
		return label
	}
	return label + "\n\n" + doc
}

// markParameter wraps the active parameter's span in inline-code backticks.
// The offsets are UTF-16 code units into the label, converted to byte offsets
// with the same mapping every LSP position uses; a span that does not land on
// a real boundary is left unmarked rather than slicing a rune.
func markParameter(label string, p Parameter) string {
	if !p.HasOffsets {
		return label
	}
	lo, hi := FromUTF16(label, p.Start), FromUTF16(label, p.End)
	if lo >= hi || hi > len(label) {
		return label
	}
	return label[:lo] + "`" + label[lo:hi] + "`" + label[hi:]
}
