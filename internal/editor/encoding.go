package editor

import (
	"fmt"
	"path/filepath"
	"strings"
)

// bom is the UTF-8 byte order mark. Not needed by anything on a unix system,
// but Windows tooling writes it and expects to read it back, and a file that
// loses it can stop being recognised by the program that owns it.
const bom = "\ufeff"

// Encoding is what a file's bytes look like around the text.
//
// The buffer holds LF-only text with no mark, because everything above it —
// the line index, the wrap engine, the piece table's offsets, every column
// calculation — would otherwise have to know that a line ending is sometimes
// two bytes. Carrying that through would be a per-keystroke tax paid to
// represent something no editing operation cares about.
//
// So the shape is decode on open, encode on save, and this struct is the only
// thing that remembers the difference.
type Encoding struct {
	CRLF bool // line endings are \r\n
	BOM  bool // the file starts with a UTF-8 byte order mark

	// Mixed records that the file used both endings. There is no way to put
	// that back byte for byte once the buffer is normalised, so raj writes the
	// dominant one and says so rather than quietly rewriting half the lines.
	Mixed bool
}

// decode strips what the buffer should not hold and reports what was stripped.
func decode(data string) (string, Encoding) {
	var e Encoding
	if strings.HasPrefix(data, bom) {
		e.BOM = true
		data = data[len(bom):]
	}
	crlf := strings.Count(data, "\r\n")
	if crlf > 0 {
		lf := strings.Count(data, "\n")
		e.CRLF = crlf*2 >= lf // ties go to CRLF: one \r\n and one \n is a CRLF file with a stray line
		e.Mixed = crlf != lf
		data = stripCR(data, crlf)
	}
	return data, e
}

// stripCR drops the carriage return of every \r\n pair, leaving lone carriage
// returns — which are data, not line endings — where they are.
//
// "\r\r\n" is the case worth knowing about, and the fuzzer found it in under a
// second: a stray CR followed by a real ending decodes to "\r\n", so the buffer
// can hold a CRLF sequence even though it holds no CRLF *ending*. That is not a
// bug, it is the ambiguity in the file — encode puts the same three bytes back —
// but it does mean "the buffer never contains \r\n" is not an invariant worth
// asserting. The round trip is.
func stripCR(s string, pairs int) string {
	var b strings.Builder
	b.Grow(len(s) - pairs)
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// encode puts it back.
func encode(text string, e Encoding) []byte {
	if e.CRLF {
		// The text is LF-only by construction, so a plain replace cannot
		// produce \r\r\n.
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	if e.BOM {
		text = bom + text
	}
	return []byte(text)
}

// EncodingWarning describes a file whose line endings raj cannot reproduce
// exactly. Empty for everything else, which is nearly every file.
//
// The alternative was to normalise silently. A mixed-ending file is usually a
// merge artifact the author wants to know about, and the one thing an editor
// must not do is change lines the user did not touch without saying so.
func (f *File) EncodingWarning() string {
	if !f.Enc.Mixed {
		return ""
	}
	ending := "LF"
	if f.Enc.CRLF {
		ending = "CRLF"
	}
	return fmt.Sprintf("%s: mixed line endings; saving will write %s throughout",
		filepath.Base(f.Path), ending)
}
