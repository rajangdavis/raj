package ui

import "unicode"

// RuneWidth reports the display columns a rune occupies: 2 for East Asian wide
// and fullwidth characters and most emoji, 0 for combining marks, 1 otherwise.
//
// This is deliberately a small table rather than a dependency. Column
// arithmetic has to agree with the terminal's, and a compact explicit table is
// easier to correct against observed Ghostty behaviour than a large one whose
// disagreements are buried. Ambiguous-width characters are treated as narrow,
// which matches Ghostty's default.
func RuneWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0 // control
	case r < 0x300:
		return 1 // Latin fast path
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0 // combining marks and format characters
	case inRanges(r, wide):
		return 2
	}
	return 1
}

func inRanges(r rune, table [][2]rune) bool {
	lo, hi := 0, len(table)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case r < table[mid][0]:
			hi = mid - 1
		case r > table[mid][1]:
			lo = mid + 1
		default:
			return true
		}
	}
	return false
}

// wide lists the double-width ranges, sorted ascending for binary search.
var wide = [][2]rune{
	{0x1100, 0x115F},   // Hangul Jamo initial consonants
	{0x2E80, 0x303E},   // CJK radicals, Kangxi, CJK symbols
	{0x3041, 0x33FF},   // Hiragana, Katakana, Bopomofo, Hangul compat, CJK compat
	{0x3400, 0x4DBF},   // CJK extension A
	{0x4E00, 0x9FFF},   // CJK unified ideographs
	{0xA000, 0xA4CF},   // Yi
	{0xA960, 0xA97F},   // Hangul Jamo extended-A
	{0xAC00, 0xD7A3},   // Hangul syllables
	{0xF900, 0xFAFF},   // CJK compatibility ideographs
	{0xFE10, 0xFE19},   // vertical forms
	{0xFE30, 0xFE6F},   // CJK compatibility forms
	{0xFF00, 0xFF60},   // fullwidth forms
	{0xFFE0, 0xFFE6},   // fullwidth signs
	{0x1F300, 0x1F64F}, // emoji: symbols and pictographs, emoticons
	{0x1F900, 0x1F9FF}, // emoji: supplemental symbols
	{0x20000, 0x2FFFD}, // CJK extension B and beyond
	{0x30000, 0x3FFFD},
}
