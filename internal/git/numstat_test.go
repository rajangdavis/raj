package git

import "testing"

// TestParseNumStatEdges pins the parser the `intent diff` read reuses when it
// was factored out of NumStat: a binary row reports "-" for both counts and is
// marked Binary, a rename keeps git's "old => new" path verbatim, a malformed
// row is skipped, and an empty patch yields no entries.
func TestParseNumStatEdges(t *testing.T) {
	got := parseNumStat([]byte("-\t-\tlogo.png\n2\t1\told.go => new.go\nnot a row\n\n3\t4\ta.go\n"))
	if len(got) != 3 {
		t.Fatalf("entries = %+v, want three rows (the malformed row skipped)", got)
	}
	if !got[0].Binary || got[0].Path != "logo.png" || got[0].Additions != 0 || got[0].Deletions != 0 {
		t.Errorf("binary row = %+v, want logo.png Binary with no counts", got[0])
	}
	if got[1].Binary || got[1].Path != "old.go => new.go" || got[1].Additions != 2 || got[1].Deletions != 1 {
		t.Errorf("rename row = %+v, want the path verbatim at 2/1", got[1])
	}
	if got[2].Path != "a.go" || got[2].Additions != 3 || got[2].Deletions != 4 {
		t.Errorf("plain row = %+v, want a.go at 3/4", got[2])
	}
	if empty := parseNumStat([]byte("")); len(empty) != 0 {
		t.Errorf("empty patch = %+v, want no entries", empty)
	}
}
