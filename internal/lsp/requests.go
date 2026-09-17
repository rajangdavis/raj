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

// parts returns the URI and range a wire location names, whichever of the
// Location and LocationLink spellings the server chose. The selection range
// is the identifier itself; the target range is the whole declaration
// including its doc comment. Jumping to the identifier is what "where is this
// defined" means, so the selection wins when both are present.
func (w wireLocation) parts() (string, *Range) {
	uri, r := w.URI, w.Range
	if uri == "" {
		uri = w.TargetURI
	}
	if w.TargetSelected != nil {
		r = w.TargetSelected
	} else if r == nil {
		r = w.TargetRange
	}
	return uri, r
}

func (w wireLocation) toLocation() (Location, bool) {
	uri, r := w.parts()
	if uri == "" || r == nil {
		return Location{}, false
	}
	return Location{Path: Path(uri), Range: *r}, true
}

// symbolLocation reads the location of a workspace symbol. A range is optional
// there: the specification allows a document URI alone, and such a symbol is
// kept with a path but no place, so choosing it opens the file at the top
// rather than being left out of the list.
func symbolLocation(raw json.RawMessage) (Location, bool) {
	if isNull(raw) {
		return Location{}, false
	}
	var w wireLocation
	if json.Unmarshal(raw, &w) != nil {
		return Location{}, false
	}
	uri, r := w.parts()
	if uri == "" {
		return Location{}, false
	}
	if r == nil {
		return Location{Path: Path(uri)}, false
	}
	return Location{Path: Path(uri), Range: *r}, true
}

// WorkspaceSymbol is one workspace/symbol result: a named declaration
// somewhere in the project.
//
// It is a small typed result rather than a []Location plus a parallel label
// slice, because the name, kind and container are what make the list readable
// and a second slice is a second thing to keep in step. Location reuses the
// path-and-range a definition names; HasRange is separate because the
// specification lets a workspace symbol carry a document URI and nothing else
// (the Location | {uri} form), and such a symbol must be listed, not dropped.
type WorkspaceSymbol struct {
	Name      string
	Kind      SymbolKind
	Container string
	Location  Location
	HasRange  bool
}

// SymbolKind is the protocol's symbol kind number. It shares the 1..26 range
// with CompletionItemKind because the specification numbers two different
// enums over the same span, not because the meanings match, so it is its own
// type with its own names.
type SymbolKind int

// String names the kind for a list row. An unknown number reads as "" rather
// than a guess, the way an unknown completion kind does.
func (k SymbolKind) String() string {
	switch k {
	case 1:
		return "file"
	case 2:
		return "module"
	case 3:
		return "namespace"
	case 4:
		return "package"
	case 5:
		return "class"
	case 6:
		return "method"
	case 7:
		return "property"
	case 8:
		return "field"
	case 9:
		return "constructor"
	case 10:
		return "enum"
	case 11:
		return "interface"
	case 12:
		return "function"
	case 13:
		return "variable"
	case 14:
		return "constant"
	case 15:
		return "string"
	case 16:
		return "number"
	case 17:
		return "boolean"
	case 18:
		return "array"
	case 19:
		return "object"
	case 20:
		return "key"
	case 21:
		return "null"
	case 22:
		return "enum member"
	case 23:
		return "struct"
	case 24:
		return "event"
	case 25:
		return "operator"
	case 26:
		return "type parameter"
	}
	return ""
}

// wireSymbol is the common shape of SymbolInformation and WorkspaceSymbol:
// both carry a name, a kind and a location, and differ only in how the
// location may be spelled.
type wireSymbol struct {
	Name          string          `json:"name"`
	Kind          int             `json:"kind"`
	ContainerName string          `json:"containerName"`
	Location      json.RawMessage `json:"location"`
}

// RequestWorkspaceSymbols asks the whole project for declarations matching a
// query.
//
// The query is the server's to match, not the client's: servers index by name
// and rank by scope and type, which is the entire reason to ask. An empty
// query is legal and means "everything you have"; the picker filters that list
// as the user types, which is why the human path passes "".
func RequestWorkspaceSymbols(ctx context.Context, c *Conn, query string) ([]WorkspaceSymbol, error) {
	if c == nil {
		return nil, ErrClosed
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "workspace/symbol", map[string]any{"query": query}, &raw); err != nil {
		return nil, err
	}
	return decodeWorkspaceSymbols(raw), nil
}

// decodeWorkspaceSymbols reads the array, or null. Both SymbolInformation and
// the newer WorkspaceSymbol are arrays of the wireSymbol shape, so one decoder
// covers servers that send either; a symbol whose location carries no range is
// kept, with HasRange false, rather than dropped.
func decodeWorkspaceSymbols(raw json.RawMessage) []WorkspaceSymbol {
	if isNull(raw) {
		return nil
	}
	var many []wireSymbol
	if json.Unmarshal(raw, &many) != nil {
		return nil
	}
	var out []WorkspaceSymbol
	for _, w := range many {
		if w.Name == "" {
			continue
		}
		loc, hasRange := symbolLocation(w.Location)
		if loc.Path == "" {
			continue
		}
		out = append(out, WorkspaceSymbol{
			Name:      w.Name,
			Kind:      SymbolKind(w.Kind),
			Container: w.ContainerName,
			Location:  loc,
			HasRange:  hasRange,
		})
	}
	return out
}

// RequestDefinition asks where the thing at a position is defined.
//
// The result may be a single location, an array of them, or null — all three
// are legal, and a server that returns one location for most symbols will
// return an array for an interface method. An empty result is "not found",
// which is normal rather than an error.
func RequestDefinition(ctx context.Context, c *Conn, path string, p Position) ([]Location, error) {
	return requestLocations(ctx, c, "textDocument/definition", path, p)
}

// requestLocations is the shape every "ask about a position, get places back"
// request shares: send the method for the position and decode the answer
// permissively. Declaration, definition, type definition and implementation
// differ only in the method name, so the wire handling lives here once rather
// than once per method.
func requestLocations(ctx context.Context, c *Conn, method, path string, p Position) ([]Location, error) {
	if c == nil {
		return nil, ErrClosed
	}
	var raw json.RawMessage
	if err := c.Call(ctx, method, positionParams(path, p), &raw); err != nil {
		return nil, err
	}
	return decodeLocations(raw), nil
}

// RequestDeclaration asks where the thing at a position is declared. It is
// definition's twin and often the same place; where they differ, the
// declaration is where the name is introduced and the definition supplies the
// body (a prototype versus its definition).
//
// The result is a location, an array of them, or null, decoded the same way a
// definition is: an empty list is "not found", which is normal rather than an
// error.
func RequestDeclaration(ctx context.Context, c *Conn, path string, p Position) ([]Location, error) {
	return requestLocations(ctx, c, "textDocument/declaration", path, p)
}

// RequestTypeDefinition asks where the type of the thing at a position is
// defined: the struct behind a variable, the interface behind a method value.
// An empty answer is "not found" rather than an error.
func RequestTypeDefinition(ctx context.Context, c *Conn, path string, p Position) ([]Location, error) {
	return requestLocations(ctx, c, "textDocument/typeDefinition", path, p)
}

// RequestImplementation asks for the concrete implementations of the interface
// or interface method at a position. Several is the normal answer — an
// interface usually has more than one implementation — and the list is decoded
// like a definition's.
func RequestImplementation(ctx context.Context, c *Conn, path string, p Position) ([]Location, error) {
	return requestLocations(ctx, c, "textDocument/implementation", path, p)
}

// RequestReferences asks for every place the symbol at a position is used.
//
// The result is a location array or null, decoded the same way a definition is:
// an empty list means "no references", which is normal rather than an error.
//
// includeDeclaration includes the declaration itself. The editor passes true:
// "who calls this" is most useful when the list also names the definition the
// callers converge on.
func RequestReferences(ctx context.Context, c *Conn, path string, p Position, includeDeclaration bool) ([]Location, error) {
	if c == nil {
		return nil, ErrClosed
	}
	params := positionParams(path, p)
	params["context"] = map[string]any{"includeDeclaration": includeDeclaration}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/references", params, &raw); err != nil {
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
