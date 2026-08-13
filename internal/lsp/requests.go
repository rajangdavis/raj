package lsp

import (
	"context"
	"encoding/json"
	"strings"
)

// Hover and go-to-definition. Both are "ask about a point in a document", and
// both have responses the specification has accumulated several shapes for over
// the years — servers in the wild send all of them, so the decoding here is
// deliberately permissive about shape and strict about meaning.

// Hover is what a server says about a position: some text, and optionally the
// span it describes.
type Hover struct {
	Text  string
	Range *Range
}

// hoverResponse is the wire shape. Contents has had three legal forms across
// versions of the protocol — a marked-up string, an object with a language and
// a value, and an array of either — and servers still send all three, so it is
// decoded as raw JSON and interpreted afterwards rather than typed here.
type hoverResponse struct {
	Contents json.RawMessage `json:"contents"`
	Range    *Range          `json:"range"`
}

// RequestHover asks what is at a position.
//
// A nil hover with no error is the normal answer for "nothing here", which is
// most positions in most files. It is not a failure and must not be reported as
// one: showing an error every time the cursor rests on a comma would make the
// feature unusable.
func RequestHover(ctx context.Context, c *Conn, path string, p Position) (*Hover, error) {
	if c == nil {
		return nil, ErrClosed
	}
	var raw json.RawMessage
	err := c.Call(ctx, "textDocument/hover", positionParams(path, p), &raw)
	if err != nil {
		return nil, err
	}
	if isNull(raw) {
		return nil, nil
	}
	var res hoverResponse
	if json.Unmarshal(raw, &res) != nil {
		return nil, nil
	}
	text := hoverText(res.Contents)
	if text == "" {
		return nil, nil
	}
	return &Hover{Text: text, Range: res.Range}, nil
}

// hoverText flattens the several legal shapes of hover contents into plain
// text.
//
// Markdown fences are stripped rather than rendered. raj draws into a terminal
// cell grid with no rich text, and a hover showing literal backticks and
// language tags is worse than one showing the signature it was wrapping.
func hoverText(raw json.RawMessage) string {
	if isNull(raw) {
		return ""
	}
	// A plain string, or a MarkupContent object.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return cleanMarkup(s)
	}
	var obj struct {
		Kind     string `json:"kind"`
		Value    string `json:"value"`
		Language string `json:"language"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Value != "" {
		return cleanMarkup(obj.Value)
	}
	// An array of either, which is the oldest form. Joined rather than reduced
	// to the first: servers use the tail for the doc comment, which is the
	// half worth reading.
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		var parts []string
		for _, it := range items {
			if t := hoverText(it); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// cleanMarkup removes code fences and collapses the blank-line runs that
// markdown uses for spacing, which a fixed-height popup cannot afford.
func cleanMarkup(s string) string {
	var out []string
	blank := 0
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimRight(line, " \t")
		if strings.HasPrefix(strings.TrimSpace(t), "```") {
			continue
		}
		if strings.TrimSpace(t) == "" {
			blank++
			if blank > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, t)
	}
	// Trim leading and trailing blanks, which fences leave behind.
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// Location is a place in a file, as a definition result names it.
type Location struct {
	Path  string
	Range Range
}

// wireLocation covers both Location and LocationLink, which servers choose
// between freely. The link form names the target range under a different key,
// so both are decoded and whichever is present wins.
type wireLocation struct {
	URI            string `json:"uri"`
	Range          *Range `json:"range"`
	TargetURI      string `json:"targetUri"`
	TargetRange    *Range `json:"targetRange"`
	TargetSelected *Range `json:"targetSelectionRange"`
}

func (w wireLocation) toLocation() (Location, bool) {
	uri, r := w.URI, w.Range
	if uri == "" {
		uri = w.TargetURI
	}
	// The selection range is the identifier itself; the target range is the
	// whole declaration including its doc comment. Jumping to the identifier
	// is what someone asking "where is this defined" means, so it wins when
	// both are present.
	if w.TargetSelected != nil {
		r = w.TargetSelected
	} else if r == nil {
		r = w.TargetRange
	}
	if uri == "" || r == nil {
		return Location{}, false
	}
	return Location{Path: Path(uri), Range: *r}, true
}

// RequestDefinition asks where the thing at a position is defined.
//
// The result may be a single location, an array of them, or null — all three
// are legal, and a server that returns one location for most symbols will
// return an array for an interface method. An empty result is "not found",
// which is normal rather than an error.
func RequestDefinition(ctx context.Context, c *Conn, path string, p Position) ([]Location, error) {
	if c == nil {
		return nil, ErrClosed
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/definition", positionParams(path, p), &raw); err != nil {
		return nil, err
	}
	return decodeLocations(raw), nil
}

func decodeLocations(raw json.RawMessage) []Location {
	if isNull(raw) {
		return nil
	}
	var one wireLocation
	if json.Unmarshal(raw, &one) == nil {
		if loc, ok := one.toLocation(); ok {
			return []Location{loc}
		}
	}
	var many []wireLocation
	if json.Unmarshal(raw, &many) == nil {
		var out []Location
		for _, w := range many {
			if loc, ok := w.toLocation(); ok {
				out = append(out, loc)
			}
		}
		return out
	}
	return nil
}

// positionParams is the shape both requests take.
func positionParams(path string, p Position) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": URI(path)},
		"position":     p,
	}
}

func isNull(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null"
}
