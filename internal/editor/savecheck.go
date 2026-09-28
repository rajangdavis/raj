package editor

import (
	"go/parser"
	"go/token"
	"strings"
)

// SaveCheck selects how much composition checking Save and SaveOver apply
// before they write. It is the resolved value of the save_check setting.
//
// The guard exists because a save writes the agreed composition, not the
// buffer on screen: when a proposed set is rejected, or a later edit moves it
// past its members, the composed text can differ from the view. There are
// exactly two modes, and neither guesses code from bytes: off writes whatever
// the session produced, and parse runs go/parser on a .go file and refuses the
// first parse error.
type SaveCheck uint8

const (
	// SaveCheckOff writes whatever composition the session produced. It is the
	// default.
	SaveCheckOff SaveCheck = iota
	// SaveCheckParse parses a .go path with go/parser and refuses the first
	// parse error. Any other path is unchecked.
	SaveCheckParse
)

// String is the setting spelling of the mode, the form the settings pane and
// the store round-trip. An unknown value spells the default.
func (m SaveCheck) String() string {
	switch m {
	case SaveCheckParse:
		return "parse"
	default:
		return "off"
	}
}

// ParseSaveCheck maps a setting value to a mode, reporting false for a spelling
// it does not know. The value is the default, SaveCheckOff, in that case, so a
// caller that only wants a mode can ignore the flag; the settings resolver uses
// the flag to report a bad value instead of silently accepting one.
func ParseSaveCheck(s string) (SaveCheck, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return SaveCheckOff, true
	case "parse":
		return SaveCheckParse, true
	default:
		return SaveCheckOff, false
	}
}

// SaveCheckError reports a save refused because the composed text failed the
// active composition guard. Reason names the first problem found and is
// surfaced verbatim by the save paths.
type SaveCheckError struct {
	Reason string
}

func (e *SaveCheckError) Error() string { return "save refused: " + e.Reason }

// CheckSaveText reports whether text is safe to write to path under mode.
//
// SaveCheckOff accepts anything. SaveCheckParse parses a .go path with
// go/parser and refuses the first parse error; for any other path it does no
// check, because raj has no parser for that language. There is deliberately no
// heuristic mode: guessing at code from bytes refused legitimate saves, and a
// false positive there is worse than no check at all.
func CheckSaveText(path, text string, mode SaveCheck) error {
	switch mode {
	case SaveCheckOff:
		return nil
	case SaveCheckParse:
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, path, text, 0); err != nil {
			return &SaveCheckError{Reason: err.Error()}
		}
		return nil
	default:
		return nil
	}
}
