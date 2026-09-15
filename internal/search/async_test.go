package search

import (
	"context"
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
	p.search = func(context.Context, string, Query) Result {
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
	p.search = func(_ context.Context, _ string, q Query) Result {
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
	started := make(chan struct{})
	p.search = func(_ context.Context, _ string, q Query) Result {
		if q.Text == "f" {
			close(started)
			<-slow // the first search finishes last
		}
		return Result{Files: len(q.Text)}
	}

	typeQuery(p, "f")
	// Waiting for the first search to be RUNNING, not merely scheduled, is
	// what makes this test test anything. run() stops the pending timer when
	// the next keystroke arrives, so on an unloaded machine the "f" search is
	// normally discarded before it ever starts — and then there is no stale
	// result, close(slow) releases nobody, and the assertion at the bottom
	// passes against a case that never happened.
	<-started
	typeQuery(p, "unc") // query is now "func"

	// Settle is the wrong wait here. It waits for every search to finish,
	// including the one deliberately blocked above, so once the first search
	// really starts Settle can only ever time out — which is what it did on a
	// loaded runner while passing everywhere else.
	waitFor(t, "the later result to arrive", func() bool {
		p.apply()
		return p.Result.Files == 4
	})

	// Now let the stale result land. Waiting for the walk to finish rather
	// than sleeping for a plausible interval is what keeps this deterministic
	// when the machine is busy.
	close(slow)
	waitFor(t, "the stale search to finish", func() bool { return p.InFlight() == 0 })
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
	p.search = func(context.Context, string, Query) Result { return Result{Files: 3} }
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

// The pane paints a walk as it goes: RunStream hands it one file's matches at a
// time, deliver parks them under the worker mutex, and apply installs them on
// the next pass of the event loop, before the finished result exists. This
// drives that half of the streaming seam directly — the fakes above replace the
// searcher, so none of them exercises deliver.
func TestPartialBatchesInstallBeforeTheResult(t *testing.T) {
	p := NewPane(t.TempDir())
	p.Debounce = time.Nanosecond

	// Hold the finished result back, so only batches may arrive. An empty
	// walk would otherwise park at once and apply would replace the partials
	// with it.
	release := make(chan struct{})
	p.search = func(context.Context, string, Query) Result {
		<-release
		return Result{}
	}
	var wakes int32
	p.Notify = func() { atomic.AddInt32(&wakes, 1) }

	p.query.Text = "needle"
	p.run()
	p.mu.Lock()
	gen := p.gen
	p.mu.Unlock()

	a := filepath.Join(p.Root, "a.go")
	b := filepath.Join(p.Root, "b.go")

	p.deliver(gen, []Match{{Path: a, Line: 1, Text: "needle a"}})
	p.apply()
	if got := len(p.Result.Matches); got != 1 {
		t.Fatalf("after the first batch: %d matches, want 1", got)
	}
	if rows := p.Rows(); len(rows) != 2 || !rows[0].IsHdr || rows[0].Path != a || rows[0].Count != 1 {
		t.Fatalf("first batch rows = %+v, want a.go's header and its one match", rows)
	}

	// A second file's batch appends at the tail: the walk is lexical, so the
	// rows above it do not move and a selection survives the arrival.
	p.deliver(gen, []Match{
		{Path: b, Line: 2, Text: "needle b"},
		{Path: b, Line: 9, Text: "needle c"},
	})
	p.apply()
	if got := len(p.Result.Matches); got != 3 {
		t.Fatalf("after the second batch: %d matches, want 3", got)
	}
	if p.Result.Files != 2 {
		t.Errorf("files = %d, want 2 (one per batch)", p.Result.Files)
	}
	rows := p.Rows()
	if len(rows) != 5 || !rows[0].IsHdr || !rows[2].IsHdr {
		t.Fatalf("rows = %+v, want header, match, header, match, match", rows)
	}
	if rows[0].Path != a || rows[2].Path != b {
		t.Errorf("walk order lost: %s then %s", rows[0].Path, rows[2].Path)
	}

	// A new query supersedes the walk above. A batch still arriving for the
	// old generation must not be painted over the current list.
	p.query.Text = "other"
	p.run()
	p.deliver(gen, []Match{{Path: a, Line: 5, Text: "late"}})
	p.apply()
	if got := len(p.Result.Matches); got != 3 {
		t.Errorf("a superseded batch changed the list: %d matches", got)
	}

	// The first batch of the new generation starts the list over rather than
	// appending to the superseded query's answer.
	p.mu.Lock()
	fresh := p.gen
	p.mu.Unlock()
	p.deliver(fresh, []Match{{Path: b, Line: 1, Text: "fresh"}})
	p.apply()
	if got := p.Result.Matches; len(got) != 1 || got[0].Text != "fresh" {
		t.Errorf("a new generation did not replace the list: %+v", got)
	}

	if atomic.LoadInt32(&wakes) == 0 {
		t.Error("deliver never notified; the pane would wait for the next tick")
	}

	close(release)
	waitFor(t, "the held searches to finish", func() bool { return p.InFlight() == 0 })
	p.stopForTest()
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
