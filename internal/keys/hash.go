package keys

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// The generated configs go stale the moment Bindings or Reclaim changes, and a
// stale config fails invisibly: the chord reaches the terminal, the terminal
// does what it always did, and raj never hears about it. cmd+n opening a
// Ghostty window instead of a raj tab is what that looks like.
//
// So every emitter stamps the table it was generated from, and raj compares the
// stamp on the installed file against its own at startup. The hash covers every
// field of every emitted binding, including Note: a note only reaches a Ghostty
// comment, but "the table changed" is a rule that needs no exceptions, and
// regenerating after a comment edit costs nothing.

// HashMarker is the literal that precedes the stamp in a generated file. Both
// emitters use it and InstalledHash looks for it, so the comment syntax of the
// target does not matter — Ghostty's '#' and iTerm2's JSON string both work.
const HashMarker = "raj-bindings-hash: "

// Hash fingerprints the emitted binding table. Short because it is read by
// people, in a comment, and its job is inequality rather than security.
func Hash() string {
	h := sha256.New()
	for _, b := range Emitted() {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\n",
			b.Group, b.Action, b.Chord, b.Seq, b.Mac, b.Linux, b.Note)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// InstalledHash reads the stamp out of a generated file. An empty return means
// the file has none, which is either a hand-written file or one generated
// before stamping existed; a caller should treat that as unknown rather than as
// stale, since it cannot tell those apart.
func InstalledHash(content string) string {
	i := strings.Index(content, HashMarker)
	if i < 0 {
		return ""
	}
	rest := content[i+len(HashMarker):]
	end := 0
	for end < len(rest) && isHex(rest[end]) {
		end++
	}
	return rest[:end]
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
}
