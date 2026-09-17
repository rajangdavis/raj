package editor

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// bom is the UTF-8 byte order mark. Not needed by anything on a unix system,
// but Windows tooling writes it and expects to read it back, and a file that
// loses it can stop being recognised by the program that owns it.
const bom = "\ufeff"

// Byte order marks. The UTF-32 marks are here only so a UTF-32 file is refused
// by name instead of being mistaken for UTF-16 — the little-endian mark starts
// with the UTF-16 little-endian one — or for binary noise.
var (
	utf8BOM    = []byte{0xEF, 0xBB, 0xBF}
	utf16LEBOM = []byte{0xFF, 0xFE}
	utf16BEBOM = []byte{0xFE, 0xFF}
	utf32LEBOM = []byte{0xFF, 0xFE, 0x00, 0x00}
	utf32BEBOM = []byte{0x00, 0x00, 0xFE, 0xFF}
)

// ErrUnsupportedEncoding reports a text encoding raj can identify but cannot
// reproduce byte for byte. Open and Reload refuse it rather than decoding it as
// something else and writing the mangled text back under the original name.
var ErrUnsupportedEncoding = errors.New("unsupported file encoding")

// ErrUnencodableText reports an edit that cannot be written in the file's
// encoding: the buffer holds a character the encoding has no byte for. Save
// refuses rather than substituting a placeholder or switching encodings
// silently, either of which would write a file that is not what is on screen.
var ErrUnencodableText = errors.New("text cannot be saved in the file's encoding")

// Kind names the character encoding of a file's bytes. UTF8 is the zero value,
// so an Encoding built before this field existed, or by hand, means UTF-8.
type Kind uint8

const (
	UTF8        Kind = iota // UTF-8, with or without a BOM
	UTF16LE                 // UTF-16, little endian, with its BOM
	UTF16BE                 // UTF-16, big endian, with its BOM
	Windows1252             // Windows code page 1252
	Latin1                  // ISO-8859-1
)

func (k Kind) String() string {
	switch k {
	case UTF16LE:
		return "UTF-16LE"
	case UTF16BE:
		return "UTF-16BE"
	case Windows1252:
		return "Windows-1252"
	case Latin1:
		return "Latin-1"
	default:
		return "UTF-8"
	}
}

// Encoding is what a file's bytes look like around the text.
//
// The buffer holds UTF-8 text with LF-only lines and no mark, because
// everything above it — the line index, the wrap engine, the piece table's
// offsets, every column calculation — would otherwise have to know that a line
// ending is sometimes two bytes or that a character is sometimes two or four.
// Carrying that through would be a per-keystroke tax paid to represent
// something no editing operation cares about.
//
// So the shape is decode on open, encode on save, and this struct is the only
// thing that remembers the difference.
type Encoding struct {
	// Kind is the character encoding. It is UTF-8 unless something in the bytes
	// said otherwise, which is what makes it safe as the zero value.
	Kind Kind

	CRLF bool // line endings are \r\n
	BOM  bool // the file starts with its encoding's byte order mark

	// Mixed records that the file used both endings. There is no way to put
	// that back byte for byte once the buffer is normalised, so raj writes the
	// dominant one and says so rather than quietly rewriting half the lines.
	Mixed bool
}

// cp1252 maps each byte to the rune Windows code page 1252 gives it. The five
// bytes CP1252 leaves undefined (0x81, 0x8D, 0x8F, 0x90, 0x9D) fall back to
// the C1 control at the same code point, which is what Latin-1 calls them;
// that keeps the table a bijection, so every byte round-trips even where the
// label is a guess.
var (
	cp1252    = [256]rune{}
	cp1252Rev = map[rune]byte{}
)

func init() {
	for b := range cp1252 {
		cp1252[b] = rune(b)
	}
	for _, m := range []struct {
		lo  byte
		run rune
	}{
		{0x80, 0x20AC}, {0x82, 0x201A}, {0x83, 0x0192}, {0x84, 0x201E},
		{0x85, 0x2026}, {0x86, 0x2020}, {0x87, 0x2021}, {0x88, 0x02C6},
		{0x89, 0x2030}, {0x8A, 0x0160}, {0x8B, 0x2039}, {0x8C, 0x0152},
		{0x8E, 0x017D}, {0x91, 0x2018}, {0x92, 0x2019}, {0x93, 0x201C},
		{0x94, 0x201D}, {0x95, 0x2022}, {0x96, 0x2013}, {0x97, 0x2014},
		{0x98, 0x02DC}, {0x99, 0x2122}, {0x9A, 0x0161}, {0x9B, 0x203A},
		{0x9C, 0x0153}, {0x9E, 0x017E}, {0x9F, 0x0178},
	} {
		cp1252[m.lo] = m.run
	}
	for b, r := range cp1252 {
		if _, taken := cp1252Rev[r]; !taken {
			cp1252Rev[r] = byte(b)
		}
	}
}

// decode converts a file's bytes to the UTF-8 text the buffer holds and reports
// the encoding that got it there. It is the one character-encoding decision in
// the open path: the byte order mark, the UTF-8 check and the single-byte
// heuristic all run in charset, so the binary verdict and the decode cannot
// disagree. It refuses content that is not text (ErrBinary) and a text encoding
// raj cannot reproduce (ErrUnsupportedEncoding).
func decode(data []byte) (string, Encoding, error) {
	enc, body, err := charset(data)
	if err != nil {
		return "", Encoding{}, err
	}
	text, err := decodeCharset(body, enc.Kind)
	if err != nil {
		return "", Encoding{}, err
	}
	// Line endings are normalised after the characters are right, so the counts
	// run on UTF-8 and the lone-CR rule below stays encoding-independent.
	crlf := strings.Count(text, "\r\n")
	if crlf > 0 {
		lf := strings.Count(text, "\n")
		enc.CRLF = crlf*2 >= lf // ties go to CRLF: one \r\n and one \n is a CRLF file with a stray line
		enc.Mixed = crlf != lf
		text = stripCR(text, crlf)
	}
	return text, enc, nil
}

// charset identifies the character encoding of data and returns the bytes with
// any byte order mark removed.
func charset(data []byte) (Encoding, []byte, error) {
	switch {
	case bytes.HasPrefix(data, utf32LEBOM), bytes.HasPrefix(data, utf32BEBOM):
		return Encoding{}, nil, fmt.Errorf("%w: UTF-32", ErrUnsupportedEncoding)
	case bytes.HasPrefix(data, utf16LEBOM):
		return Encoding{Kind: UTF16LE, BOM: true}, data[2:], nil
	case bytes.HasPrefix(data, utf16BEBOM):
		return Encoding{Kind: UTF16BE, BOM: true}, data[2:], nil
	case bytes.HasPrefix(data, utf8BOM):
		return Encoding{Kind: UTF8, BOM: true}, data[3:], nil
	}
	kind, err := sniffSingleByte(data)
	if err != nil {
		return Encoding{}, nil, err
	}
	return Encoding{Kind: kind}, data, nil
}

// sniffSingleByte decides how a file with no byte order mark should be read: as
// UTF-8 when it is valid UTF-8 end to end, otherwise as the legacy single-byte
// encoding its high bytes name. Content that is not text — one holding a NUL or
// the control bytes binary formats are made of — is refused (ErrBinary), and an
// unmarked UTF-16 file is refused by name rather than mangled
// (ErrUnsupportedEncoding).
func sniffSingleByte(data []byte) (Kind, error) {
	// NUL is the one byte no text encoding uses for content. It is checked
	// before the UTF-8 fast path because NUL is valid UTF-8, and a file
	// holding one is binary whatever its bytes happen to decode to.
	head := data
	if len(head) > sniffLen {
		head = head[:sniffLen]
	}
	if bytes.IndexByte(data, 0) >= 0 {
		if looksUTF16(head) {
			return 0, fmt.Errorf("%w: UTF-16 without a byte order mark", ErrUnsupportedEncoding)
		}
		return 0, ErrBinary
	}
	if utf8.Valid(data) {
		return UTF8, nil
	}
	for _, b := range head {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' {
			return 0, ErrBinary
		}
		if b == 0x7f {
			return 0, ErrBinary
		}
	}
	// A byte in the C1 range is where CP1252 and Latin-1 disagree, and a text
	// file holding one means the Windows mapping. The bytes CP1252 leaves
	// undefined still map by identity and round-trip under this kind.
	for _, b := range head {
		if b >= 0x80 && b <= 0x9f {
			return Windows1252, nil
		}
	}
	return Latin1, nil
}

// looksUTF16 reports whether head has the signature of an unmarked UTF-16 file:
// NUL bytes on one byte parity and none on the other, in the proportion ASCII
// in UTF-16 produces. It is a heuristic and only ever causes a refusal by name,
// never a decode, so a false positive cannot corrupt a file.
func looksUTF16(head []byte) bool {
	if len(head) < 4 {
		return false
	}
	if len(head) > 64 {
		head = head[:64]
	}
	even, odd := 0, 0
	for i, b := range head {
		if b != 0 {
			continue
		}
		if i%2 == 0 {
			even++
		} else {
			odd++
		}
	}
	if even > 0 && odd > 0 {
		return false
	}
	total := even + odd
	return total > 0 && total*4 >= len(head)
}

// decodeCharset turns the body of a file into UTF-8 text.
func decodeCharset(data []byte, kind Kind) (string, error) {
	switch kind {
	case UTF16LE:
		return decodeUTF16(data, false)
	case UTF16BE:
		return decodeUTF16(data, true)
	case Windows1252:
		return decodeWindows1252(data), nil
	case Latin1:
		return decodeLatin1(data), nil
	default:
		if !utf8.Valid(data) {
			return "", fmt.Errorf("%w: invalid UTF-8", ErrUnsupportedEncoding)
		}
		return string(data), nil
	}
}

// decodeUTF16 converts UTF-16 code units to UTF-8, refusing the two shapes that
// cannot round-trip: an odd byte count, and an unpaired surrogate. Go's own
// decoder replaces a lone surrogate with U+FFFD, which would mean a save wrote
// bytes the file did not have, so the check is what keeps the byte-identity
// promise rather than a formality.
func decodeUTF16(data []byte, bigEndian bool) (string, error) {
	if len(data)%2 != 0 {
		return "", fmt.Errorf("%w: UTF-16 has an odd number of bytes", ErrUnsupportedEncoding)
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		hi, lo := data[2*i], data[2*i+1]
		if bigEndian {
			units[i] = uint16(hi)<<8 | uint16(lo)
		} else {
			units[i] = uint16(lo)<<8 | uint16(hi)
		}
	}
	for i := 0; i < len(units); i++ {
		switch u := units[i]; {
		case u >= 0xD800 && u <= 0xDBFF:
			if i+1 >= len(units) || units[i+1] < 0xDC00 || units[i+1] > 0xDFFF {
				return "", fmt.Errorf("%w: UTF-16 contains an unpaired surrogate", ErrUnsupportedEncoding)
			}
			i++ // the low half is part of this character
		case u >= 0xDC00 && u <= 0xDFFF:
			return "", fmt.Errorf("%w: UTF-16 contains an unpaired surrogate", ErrUnsupportedEncoding)
		}
	}
	return string(utf16.Decode(units)), nil
}

func decodeLatin1(data []byte) string {
	b := make([]byte, 0, len(data)+len(data)/2)
	for _, c := range data {
		if c < 0x80 {
			b = append(b, c)
			continue
		}
		b = append(b, 0xC0|c>>6, 0x80|c&0x3F)
	}
	return string(b)
}

func decodeWindows1252(data []byte) string {
	b := make([]byte, 0, len(data)+len(data)/2)
	for _, c := range data {
		b = utf8.AppendRune(b, cp1252[c])
	}
	return string(b)
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

// encode puts it back. A character with no byte in the file's encoding is an
// error, not a silent substitution: the alternative is writing a file that
// looks saved but does not hold the text on screen.
func encode(text string, e Encoding) ([]byte, error) {
	if e.CRLF {
		// The text is LF-only by construction, so a plain replace cannot
		// produce \r\r\n.
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	switch e.Kind {
	case UTF16LE:
		return encodeUTF16(text, false), nil
	case UTF16BE:
		return encodeUTF16(text, true), nil
	case Windows1252:
		return encodeWindows1252(text)
	case Latin1:
		return encodeLatin1(text)
	default:
		if e.BOM {
			return append([]byte(bom), text...), nil
		}
		return []byte(text), nil
	}
}

// encodeUTF16 writes the BOM for the byte order it is told to use. Only
// BOM-marked UTF-16 is detected, so every file this runs for had one and a save
// must keep it.
func encodeUTF16(text string, bigEndian bool) []byte {
	units := utf16.Encode([]rune(text))
	b := make([]byte, 0, 2+len(units)*2)
	if bigEndian {
		b = append(b, 0xFE, 0xFF)
	} else {
		b = append(b, 0xFF, 0xFE)
	}
	for _, u := range units {
		if bigEndian {
			b = append(b, byte(u>>8), byte(u))
		} else {
			b = append(b, byte(u), byte(u>>8))
		}
	}
	return b
}

func encodeWindows1252(text string) ([]byte, error) {
	b := make([]byte, 0, len(text))
	for _, r := range text {
		c, ok := cp1252Rev[r]
		if !ok {
			return nil, fmt.Errorf("%w: %q is not representable in Windows-1252", ErrUnencodableText, r)
		}
		b = append(b, c)
	}
	return b, nil
}

func encodeLatin1(text string) ([]byte, error) {
	b := make([]byte, 0, len(text))
	for _, r := range text {
		if r > 0xFF {
			return nil, fmt.Errorf("%w: %q is not representable in Latin-1", ErrUnencodableText, r)
		}
		b = append(b, byte(r))
	}
	return b, nil
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
