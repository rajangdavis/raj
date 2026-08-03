package piecetable

import (
	"math/rand"
	"testing"
)

func sessions(orig string) map[string]*Session {
	return map[string]*Session{
		"naive": NewSession(NewNaive(orig)),
		"doc":   NewSession(NewDoc(orig, 5)),
	}
}

func text(s *Session) string { return s.Buffer().Slice(0, s.Buffer().Len()) }

// A diff written against an untouched version applies verbatim.
func TestApplyDiffClean(t *testing.T) {
	for name, s := range sessions("aaa bbb ccc") {
		base := s.Version()
		_, conflicts := s.ApplyDiff(Agent, base, []Hunk{
			{Start: 0, End: 3, Text: "XXX"},
			{Start: 8, End: 11, Text: "ZZZ"},
		})
		if len(conflicts) != 0 {
			t.Fatalf("%s: unexpected conflicts %+v", name, conflicts)
		}
		if got := text(s); got != "XXX bbb ZZZ" {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

// Offsets written against an old version are carried forward, not applied
// literally: the user typing earlier in the file must not misplace agent edits.
func TestApplyDiffRebasesStaleOffsets(t *testing.T) {
	for name, s := range sessions("aaa bbb ccc") {
		base := s.Version()
		s.Insert(User, 0, ">>>") // ">>>aaa bbb ccc", shifts everything by 3
		_, conflicts := s.ApplyDiff(Agent, base, []Hunk{{Start: 8, End: 11, Text: "ZZZ"}})
		if len(conflicts) != 0 {
			t.Fatalf("%s: unexpected conflicts %+v", name, conflicts)
		}
		if got := text(s); got != ">>>aaa bbb ZZZ" {
			t.Errorf("%s: got %q, want stale offset rebased", name, got)
		}
	}
}

// A hunk whose range someone else edited is rejected — and only that hunk.
func TestApplyDiffRejectsOnlyTheConflictingHunk(t *testing.T) {
	for name, s := range sessions("aaa bbb ccc") {
		base := s.Version()
		s.Delete(User, 4, 3) // remove "bbb", the middle hunk's target
		v, conflicts := s.ApplyDiff(Agent, base, []Hunk{
			{Start: 0, End: 3, Text: "XXX"},
			{Start: 4, End: 7, Text: "YYY"},
			{Start: 8, End: 11, Text: "ZZZ"},
		})
		if len(conflicts) != 1 || conflicts[0].Index != 1 {
			t.Fatalf("%s: got conflicts %+v, want exactly hunk 1", name, conflicts)
		}
		if got := text(s); got != "XXX  ZZZ" {
			t.Errorf("%s: got %q; surviving hunks should still apply", name, got)
		}
		if v != s.Version() {
			t.Errorf("%s: returned version %d != current %d", name, v, s.Version())
		}
	}
}

// Edits exactly at a hunk boundary shift it rather than conflicting, or
// adjacent hunks would reject each other constantly.
func TestBoundaryEditsDoNotConflict(t *testing.T) {
	for name, s := range sessions("aaabbb") {
		base := s.Version()
		s.Insert(User, 3, "-") // exactly between the two ranges
		_, conflicts := s.ApplyDiff(Agent, base, []Hunk{
			{Start: 0, End: 3, Text: "X"},
			{Start: 3, End: 6, Text: "Y"},
		})
		if len(conflicts) != 0 {
			t.Fatalf("%s: unexpected conflicts %+v", name, conflicts)
		}
		if got := text(s); got != "X-Y" {
			t.Errorf("%s: got %q, want X-Y", name, got)
		}
	}
}

// Hunks within one diff are rebased against each other, so overlapping hunks in
// a single submission conflict rather than corrupting the document.
func TestOverlappingHunksInOneDiff(t *testing.T) {
	for name, s := range sessions("abcdefgh") {
		_, conflicts := s.ApplyDiff(Agent, s.Version(), []Hunk{
			{Start: 0, End: 4, Text: "1"},
			{Start: 2, End: 6, Text: "2"},
		})
		if len(conflicts) != 1 || conflicts[0].Index != 1 {
			t.Fatalf("%s: got %+v, want hunk 1 rejected", name, conflicts)
		}
		if got := text(s); got != "1efgh" {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

func TestUndoRedo(t *testing.T) {
	for name, s := range sessions("hello") {
		s.Insert(User, 5, " world")
		if got := text(s); got != "hello world" {
			t.Fatalf("%s: setup got %q", name, got)
		}
		if !s.Undo(User) {
			t.Fatalf("%s: undo refused", name)
		}
		if got := text(s); got != "hello" {
			t.Errorf("%s: after undo got %q", name, got)
		}
		if !s.Redo(User) {
			t.Fatalf("%s: redo refused", name)
		}
		if got := text(s); got != "hello world" {
			t.Errorf("%s: after redo got %q", name, got)
		}
	}
}

// Undo restores deleted text without the op having copied it: stores are
// append-only, so the inverse just points at pieces that were never erased.
func TestUndoRestoresDeletedText(t *testing.T) {
	for name, s := range sessions("keep this text") {
		s.Delete(User, 4, 5) // "keep text"
		before := s.Store().Bytes()
		if !s.Undo(User) {
			t.Fatalf("%s: undo refused", name)
		}
		if got := text(s); got != "keep this text" {
			t.Errorf("%s: got %q", name, got)
		}
		if after := s.Store().Bytes(); after != before {
			t.Errorf("%s: undo grew the store by %d bytes; it should copy nothing",
				name, after-before)
		}
	}
}

// Undo is scoped to one author but ordered on one shared timeline: cmd+z must
// reach past an agent's edit to the user's own, not undo the agent's work.
func TestUndoIsAuthorScoped(t *testing.T) {
	for name, s := range sessions("") {
		s.Insert(User, 0, "user")
		s.Insert(Agent, 4, "agent")
		if !s.Undo(User) {
			t.Fatalf("%s: undo refused", name)
		}
		if got := text(s); got != "agent" {
			t.Errorf("%s: got %q, want the user's edit undone", name, got)
		}
	}
}

// Undoing into a region someone else has since rewritten is declined rather
// than applied blindly.
func TestUndoDeclinesWhenRegionRewritten(t *testing.T) {
	for name, s := range sessions("") {
		s.Insert(User, 0, "hello")
		s.ApplyDiff(Agent, s.Version(), []Hunk{{Start: 1, End: 4, Text: "XYZ"}})
		if s.Undo(User) {
			t.Errorf("%s: undo should decline; buffer now %q", name, text(s))
		}
	}
}

// Authorship survives the journal: an agent's diff tints as agent text.
func TestDiffAttribution(t *testing.T) {
	for name, s := range sessions("aaa bbb") {
		s.ApplyDiff(Agent, s.Version(), []Hunk{{Start: 4, End: 7, Text: "ZZZ"}})
		spans := s.Buffer().Spans(0, s.Buffer().Len())
		want := []Span{{0, 4, Original}, {4, 3, Agent}}
		if len(spans) != len(want) {
			t.Fatalf("%s: got %+v", name, spans)
		}
		for i := range want {
			if spans[i] != want[i] {
				t.Errorf("%s: span %d = %+v, want %+v", name, i, spans[i], want[i])
			}
		}
	}
}

// Random interleaved user typing and agent diffs: both implementations must
// agree byte for byte, and the journal must stay a faithful record.
func TestFuzzConcurrentAuthors(t *testing.T) {
	for seed := 0; seed < 30; seed++ {
		rng := rand.New(rand.NewSource(int64(seed)))
		ss := sessions("func main() {\n\tprintln(\"hi\")\n}\n")
		stale := map[string]Version{}
		for name, s := range ss {
			stale[name] = s.Version()
		}
		for step := 0; step < 60; step++ {
			n := 0
			for _, s := range ss {
				n = s.Buffer().Len()
			}
			switch rng.Intn(4) {
			case 0:
				pos := rng.Intn(n + 1)
				for _, s := range ss {
					s.Insert(User, pos, "u")
				}
			case 1:
				if n > 0 {
					pos, l := rng.Intn(n), rng.Intn(3)+1
					for _, s := range ss {
						s.Delete(User, pos, l)
					}
				}
			case 2:
				for _, s := range ss {
					s.Undo(User)
				}
			default:
				start := rng.Intn(n + 1)
				end := start + rng.Intn(4)
				if end > n {
					end = n
				}
				for name, s := range ss {
					s.ApplyDiff(Agent, stale[name], []Hunk{{Start: start, End: end, Text: "A"}})
					stale[name] = s.Version()
				}
			}
			var want string
			first := true
			for name, s := range ss {
				got := text(s)
				if first {
					want, first = got, false
					continue
				}
				if got != want {
					t.Fatalf("seed %d step %d: %s = %q, other = %q", seed, step, name, got, want)
				}
			}
		}
	}
}
