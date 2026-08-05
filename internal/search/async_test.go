package search

import (
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"raj/internal/keys"
)

// typeQuery feeds text into the query field the way the pane receives it.
func typeQuery(p *Pane, text string) {
	for _, r := range text {
		p.Handle(keys.None, string(r))
	}
}

// A keystroke must not wait for the walk. The fake search blocks for longer
// than any interactive budget; Handle still has to return immediately.
func TestHandleDoesNotWaitForTheSearch(t *testing.T) {
	p := NewPane(t.TempDir())
	p.Debounce = time.Nanosecond
	release := make(chan struct{})
	p.search = func(string, Query) Result {
		<-release
		return Result{Files: 1}
	}

	start := time.Now()
	typeQuery(p, "needle")
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("six keystrokes took %v; the walk is still on the event thread", elapsed)
	}
	close(release)
	p.Settle(2 * time.Second)
}

// A burst of keystrokes is one search, not one per character. This is the whole
// point of the debounce: typing a six-letter word used to walk the tree six
// times, five of them for a query already superseded.
func TestDebounceCoalescesABurst(t *testing.T) {
	p := NewPane(t.TempDir())
	p.Debounce = 40 * time.Millisecond
	var searches int32
	p.search = func(_ string, q Query) Result {
		atomic.AddInt32(&searches, 1)
		return Result{Files: len(q.Text)}
	}

	typeQuery(p, "needle")
	if !p.Settle(2 * time.Second) {
		t.Fatal("search never settled")
	}
	if got := atomic.LoadInt32(&searches); got != 1 {
		t.Errorf("%d searches for one burst, want 1", got)
	}
	if p.Result.Files != len("needle") {
		t.Errorf("result is for a %d-character query, want the last one", p.Result.Files)
	}
}

// A slow early search must not overwrite a fast later one. Without the
// generation check, results land in completion order and the box shows matches
// for a query the user has already typed past.
func TestStaleResultIsDropped(t *testing.T) {
	p := NewPane(t.TempDir())
	p.Debounce = time.Nanosecond
	slow := make(chan struct{})
	p.search = func(_ string, q Query) Result {
		if q.Text == "f" {
			<-slow // the first search finishes last
		}
		return Result{Files: len(q.Text)}
	}

	typeQuery(p, "f")
	typeQuery(p, "unc") // query is now "func"
	if !p.Settle(2 * time.Second) {
		t.Fatal("later search never settled")
	}
	if p.Result.Files != 4 {
		t.Fatalf("result is for a %d-character query, want 4", p.Result.Files)
	}
	close(slow)
	time.Sleep(50 * time.Millisecond)
	p.apply()
	if p.Result.Files != 4 {
		t.Errorf("the stale result replaced the current one: %d", p.Result.Files)
	}
}

// Clearing the box clears the results without waiting for a debounce that has
// nothing to search for.
func TestEmptyQueryClearsImmediately(t *testing.T) {
	p := NewPane(t.TempDir())
	p.Debounce = time.Hour // any deferred work would never run
	p.search = func(string, Query) Result { return Result{Files: 3} }
	p.Result = Result{Files: 3, Matches: []Match{{Path: "a.go"}}}

	typeQuery(p, "x")
	p.Handle(keys.Backspace, "")
	if len(p.Result.Matches) != 0 || len(p.Rows()) != 0 {
		t.Errorf("results survived an empty query: %d matches, %d rows",
			len(p.Result.Matches), len(p.Rows()))
	}
}

// tree builds a synthetic repository: files by the hundred, most of them
// without the needle, which is the shape that makes an un-debounced search hurt.
func tree(tb testing.TB, files, lines int) string {
	tb.Helper()
	dir := tb.TempDir()
	body := make([]byte, 0, lines*40)
	for i := 0; i < lines; i++ {
		body = append(body, []byte("func handler"+strconv.Itoa(i)+"(w, r) { return nil }\n")...)
	}
	for i := 0; i < files; i++ {
		sub := filepath.Join(dir, "pkg"+strconv.Itoa(i%20))
		os.MkdirAll(sub, 0o755)
		os.WriteFile(filepath.Join(sub, "f"+strconv.Itoa(i)+".go"), body, 0o644)
	}
	return dir
}

// BenchmarkRun is the number the debounce is spending. Whatever it costs, the
// old pane paid it once per keystroke, on the thread that draws frames.
func BenchmarkRun(b *testing.B) {
	dir := tree(b, 400, 200)
	for _, q := range []Query{
		{Text: "handler7"},          // a common match: capped early
		{Text: "zzz_no_such_thing"}, // no match: walks everything, the worst case
	} {
		b.Run(q.Text, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				Run(dir, q)
			}
		})
	}
}
