// Package complete suggests words from what is already open.
//
// This is buffer-word completion, not IntelliSense: it knows nothing about
// types, scope or imports, and it will happily suggest a word from a comment as
// readily as a function name. What it does know is that the word you are typing
// almost certainly appears somewhere nearby already, which is true often enough
// to be worth a keystroke.
//
// The honest boundary is that a suggestion means "this string exists in a
// buffer you have open" and nothing more. Anything stronger needs a language
// server, and the seam is deliberately shaped so one can supply candidates
// later without the overlay changing.
package complete

import (
	"sort"
	"strings"
)

// Source is where candidates come from. A language server becomes another
// implementation rather than a rewrite: the overlay only needs strings back.
type Source interface {
	Candidates(prefix string) []Candidate
}

// Candidate is one suggestion. Detail is shown beside the word — the file it
// came from, or a kind — and is never matched against.
type Candidate struct {
	Word   string
	Detail string
	// score is how strongly this candidate is preferred. Higher is better.
	score int
}

// Scores. The ordering they produce is the whole user-visible behaviour of
// this package, so they are stated together rather than scattered.
const (
	// A declaration beats a word that merely appears somewhere, because a
	// symbol is a thing with a definition and a word is just bytes.
	scoreSymbol = 100
	// A word from the buffer being edited beats the same word from another
	// file: locality is the best signal available without types.
	scoreLocal = 40
	scoreOther = 10
)

// MinPrefix is how much has to be typed before anything is offered.
//
// One character matches most of a file and ranks it by nothing useful, so the
// list would be noise arriving on the first keystroke of every word. Two is
// enough to be discriminating and still ahead of finishing the word by hand.
const MinPrefix = 2

// MaxResults bounds the list. Past a screenful nobody is reading, and the cost
// of ranking is paid per keystroke.
const MaxResults = 12

// Words extracts identifier-like words from text.
//
// Runs of identifier bytes, which over-collects by design: the word inside a
// string literal or a comment is still a word someone typed and may well be the
// one they are typing again. Distinguishing them needs a lexer, and a lexer
// that is wrong about one language is worse than no lexer at all.
func Words(text string, into map[string]bool) map[string]bool {
	if into == nil {
		into = map[string]bool{}
	}
	start := -1
	for i := 0; i <= len(text); i++ {
		var b byte
		if i < len(text) {
			b = text[i]
		}
		switch {
		case i < len(text) && isWordByte(b):
			if start < 0 {
				start = i
			}
		case start >= 0:
			// A word starting with a digit is a number, not an identifier.
			if w := text[start:i]; !isDigit(w[0]) && len(w) > 1 {
				into[w] = true
			}
			start = -1
		}
	}
	return into
}

// Buffers is a snapshot of what is open, in the same shape search.Docs uses:
// path to contents. Current is the document being edited, whose words rank
// above the rest.
type Buffers struct {
	Current  string
	Contents map[string]string
	// Symbols are declarations, which rank above plain words. Keyed by path so
	// a suggestion can say where it came from.
	Symbols map[string][]string
}

// Rank returns the candidates matching prefix, best first.
//
// Matching is a case-sensitive prefix test rather than the fuzzy match the file
// picker uses. Completion is finishing a word you have started, so a candidate
// that does not begin with what you typed is not a completion of it — and
// inserting one would replace the letters already on screen with different
// ones. Case is significant because in most languages it is: Foo and foo are
// different identifiers.
func (b Buffers) Rank(prefix string) []Candidate {
	if len(prefix) < MinPrefix {
		return nil
	}
	best := map[string]Candidate{}
	keep := func(word, detail string, score int) {
		if word == prefix || !strings.HasPrefix(word, prefix) {
			// The word you have already typed is not a completion of itself.
			return
		}
		if old, ok := best[word]; ok && old.score >= score {
			return
		}
		best[word] = Candidate{Word: word, Detail: detail, score: score}
	}

	for path, syms := range b.Symbols {
		score := scoreSymbol
		if path == b.Current {
			score += scoreLocal
		}
		for _, w := range syms {
			keep(w, short(path), score)
		}
	}
	for path, text := range b.Contents {
		score := scoreOther
		detail := short(path)
		if path == b.Current {
			score = scoreLocal
			detail = ""
		}
		for w := range Words(text, nil) {
			keep(w, detail, score)
		}
	}

	out := make([]Candidate, 0, len(best))
	for _, c := range best {
		out = append(out, c)
	}
	// Sorted by score, then shortest, then alphabetically. Shortest first
	// because the shorter completion is the one more likely to be a prefix of
	// the others, and alphabetically last so the order is total — a list that
	// reshuffles between identical keystrokes is worse than a wrong order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if len(out[i].Word) != len(out[j].Word) {
			return len(out[i].Word) < len(out[j].Word)
		}
		return out[i].Word < out[j].Word
	})
	if len(out) > MaxResults {
		out = out[:MaxResults]
	}
	return out
}

// Candidates satisfies Source.
func (b Buffers) Candidates(prefix string) []Candidate { return b.Rank(prefix) }

// short is the file name, which is what fits beside a word in a narrow overlay.
func short(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// PrefixAt is the identifier ending at off, which is what to complete. It stops
// at the cursor rather than taking the whole word under it: completing from the
// middle of a word would replace letters to the right that were typed
// deliberately.
func PrefixAt(line string, col int) string {
	if col > len(line) {
		col = len(line)
	}
	if col < 0 {
		col = 0
	}
	lo := col
	for lo > 0 && isWordByte(line[lo-1]) {
		lo--
	}
	w := line[lo:col]
	if w != "" && isDigit(w[0]) {
		return "" // a number is not an identifier being typed
	}
	return w
}

func isWordByte(b byte) bool {
	return b == '_' || isDigit(b) ||
		b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
