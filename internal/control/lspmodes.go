package control

import "strings"

// LSPModes is every mode `raj ctl lsp` accepts, in the order the CLI documents
// them. It is the one list: the CLI's mode gate and its usage line derive from
// it, so adding a mode here is the whole client-side change. The host dispatch
// is deliberately narrower — inlay-hints, format, range-format and symbols
// reach their own host methods before host.LSP — so this list names what the
// client accepts, not what one host switch handles.
var LSPModes = []string{
	"hover", "definition", "declaration", "type-definition", "implementation",
	"references", "completion", "signature", "diagnostics", "inlay-hints", "symbols",
	"format", "range-format", "document-symbols",
}

// lspModesSentence renders LSPModes as the human list the CLI prints: comma
// separated, with the last joined by "or".
func lspModesSentence() string {
	switch len(LSPModes) {
	case 0:
		return ""
	case 1:
		return LSPModes[0]
	}
	return strings.Join(LSPModes[:len(LSPModes)-1], ", ") + " or " + LSPModes[len(LSPModes)-1]
}

// lspModeKnown reports whether mode is one LSPModes names. It is the client's
// whole mode gate.
func lspModeKnown(mode string) bool {
	for _, m := range LSPModes {
		if m == mode {
			return true
		}
	}
	return false
}
