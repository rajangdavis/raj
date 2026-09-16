package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"raj/internal/control"
	"raj/internal/lsp"
)

func diag(line, sev int, msg string) lsp.Diagnostic {
	return lsp.Diagnostic{
		Range:    lsp.Range{Start: lsp.Position{Line: line}},
		Severity: sev,
		Message:  msg,
	}
}

// A publish is the complete set for a document, so the newest replaces the
// previous one whole. Merging would accumulate problems already fixed.
func TestPublishReplacesRatherThanMerges(t *testing.T) {
	d := newDiagnostics()
	d.set("/w/a.go", []lsp.Diagnostic{diag(1, sevError, "first"), diag(2, sevError, "second")})
	d.set("/w/a.go", []lsp.Diagnostic{diag(5, sevError, "third")})

	got := d.forPath("/w/a.go")
	if len(got) != 1 || got[0].Message != "third" {
		t.Errorf("got %d items (%v), want only the newest publish", len(got), got)
	}
}

// An empty publish is how a server says the problems are fixed. Ignoring it
// would leave them on screen forever.
func TestEmptyPublishClears(t *testing.T) {
	d := newDiagnostics()
	d.set("/w/a.go", []lsp.Diagnostic{diag(1, sevError, "broken")})
	d.set("/w/a.go", nil)

	if got := d.forPath("/w/a.go"); len(got) != 0 {
		t.Errorf("got %v, want the file cleared", got)
	}
	if got := d.summary("/w/a.go"); got != "" {
		t.Errorf("summary = %q, want nothing", got)
	}
}

// A publish is the server speaking about a path, so an empty publish marks it
// published all the same. forPath cannot tell an empty publish from a path no
// server has answered about: both return nothing, and the two must not read
// the same to a caller checking for a clean file.
func TestPublishMarksPathPublished(t *testing.T) {
	d := newDiagnostics()
	if d.published("/w/never.go") {
		t.Error("an untouched path reported as published")
	}

	d.set("/w/a.go", nil) // an empty publish is still a publish
	if !d.published("/w/a.go") {
		t.Error("an empty publish did not mark the path published")
	}

	d.set("/w/b.go", []lsp.Diagnostic{diag(1, sevError, "broken")})
	if !d.published("/w/b.go") {
		t.Error("a publish with problems did not mark the path published")
	}

	d.clear("/w/a.go")
	if d.published("/w/a.go") {
		t.Error("clear left the path marked published")
	}
	if !d.published("/w/b.go") {
		t.Error("clearing one path unpublished another")
	}
}

// A store built by hand with only byPath set — the shape newDiagnostics is the
// only real constructor for — must still answer published without panicking,
// and must record a publish into a set that was never initialised.
func TestPublishedOnHandBuiltStore(t *testing.T) {
	d := &diagnostics{byPath: map[string][]lsp.Diagnostic{}}
	if d.published("/w/a.go") {
		t.Error("a hand-built store claimed a publish it never saw")
	}
	d.set("/w/a.go", nil)
	if !d.published("/w/a.go") {
		t.Error("a hand-built store did not record the publish")
	}
}

// A line with both a warning and an error is an error line. Showing the
// warning because it was published first would under-report it.
func TestMostSevereWins(t *testing.T) {
	d := newDiagnostics()
	d.set("/w/a.go", []lsp.Diagnostic{
		diag(3, sevWarning, "warn"),
		diag(3, sevError, "err"),
		diag(3, sevHint, "hint"),
	})
	got, ok := d.atLine("/w/a.go", 3)
	if !ok {
		t.Fatal("no diagnostic found")
	}
	if got.Message != "err" {
		t.Errorf("reported %q, want the error", got.Message)
	}
}

// A missing severity is treated as an error: the protocol leaves it to the
// client, and under-reporting a real problem is the worse mistake.
func TestMissingSeverityIsAnError(t *testing.T) {
	if severityRank(0) != severityRank(sevError) {
		t.Error("an unspecified severity should rank as an error")
	}
	d := newDiagnostics()
	d.set("/w/a.go", []lsp.Diagnostic{diag(1, 0, "unspecified")})
	if e, _ := d.counts("/w/a.go"); e != 1 {
		t.Errorf("counted %d errors, want 1", e)
	}
}

// Diagnostics are stored in file order so the list reads like the file, and a
// warning early does not sort above an error late.
func TestStoredInFileOrder(t *testing.T) {
	d := newDiagnostics()
	d.set("/w/a.go", []lsp.Diagnostic{
		diag(200, sevError, "late error"),
		diag(3, sevWarning, "early warning"),
		diag(50, sevInfo, "middle"),
	})
	got := d.forPath("/w/a.go")
	want := []int{3, 50, 200}
	for i, w := range want {
		if got[i].Range.Start.Line != w {
			t.Fatalf("order = %v, want lines %v", got, want)
		}
	}
}

// Files are independent: a publish for one must not disturb another.
func TestFilesAreIndependent(t *testing.T) {
	d := newDiagnostics()
	d.set("/w/a.go", []lsp.Diagnostic{diag(1, sevError, "a")})
	d.set("/w/b.go", []lsp.Diagnostic{diag(1, sevError, "b")})
	d.set("/w/a.go", nil)

	if len(d.forPath("/w/b.go")) != 1 {
		t.Error("clearing one file cleared another")
	}
}

func TestSummary(t *testing.T) {
	d := newDiagnostics()
	cases := []struct {
		items []lsp.Diagnostic
		want  string
	}{
		{nil, ""},
		{[]lsp.Diagnostic{diag(1, sevError, "e")}, "1E"},
		{[]lsp.Diagnostic{diag(1, sevWarning, "w")}, "1W"},
		{[]lsp.Diagnostic{diag(1, sevError, "e"), diag(2, sevWarning, "w")}, "1E 1W"},
		{[]lsp.Diagnostic{diag(1, sevHint, "h")}, ""}, // hints are not counted
	}
	for _, c := range cases {
		d.set("/w/a.go", c.items)
		if got := d.summary("/w/a.go"); got != c.want {
			t.Errorf("summary(%v) = %q, want %q", c.items, got, c.want)
		}
	}
}

// The status line shows the problem on the cursor's line, folded onto one line:
// a multi-line message from a type checker is common, and truncating at the
// newline hides the part that says what to do about it.
func TestStatusShowsTheDiagnosticUnderTheCursor(t *testing.T) {
	h := newHarness(t, "one\ntwo\nthree\n")
	path := h.docPath(h.Pane())
	h.diags.set(path, []lsp.Diagnostic{
		diag(1, sevError, "undefined: foo\nhave bar\nwant foo"),
	})

	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	got := h.diagnosticAtCursor()
	if !strings.Contains(got, "undefined: foo") {
		t.Errorf("got %q, want the message", got)
	}
	if strings.Contains(got, "\n") {
		t.Error("a newline reached the status line")
	}
	if !strings.Contains(got, "want foo") {
		t.Error("the message was truncated at the first line")
	}

	h.press("ctrl+g")
	h.typeText("1")
	h.press("enter")
	if got := h.diagnosticAtCursor(); got != "" {
		t.Errorf("a line with no problem reported %q", got)
	}
}

// A file with no diagnostics, and a pane with no path, report nothing rather
// than panicking.
func TestDiagnosticsDegenerateInputs(t *testing.T) {
	d := newDiagnostics()
	d.set("", nil)
	d.clear("/w/never.go")
	if _, ok := d.atLine("/w/never.go", 0); ok {
		t.Error("found a diagnostic in a file with none")
	}
	h := newHarness(t, "text\n")
	h.Pane().File.Path = ""
	if got := h.diagnosticAtCursor(); got != "" {
		t.Errorf("an unnamed buffer reported %q", got)
	}
}

// Draining is safe with no servers running, which is most of the time.
func TestDrainWithNoServers(t *testing.T) {
	h := newHarness(t, "text\n")
	h.drainDiagnostics()
	h.drainDiagnostics()
}

// The version a publish applied to travels with the set, so the diagnostics
// read can tell one that describes the current text from one that stopped
// describing it the moment the buffer changed. A publish with no version
// replaces the remembered one rather than leaving the older version to look
// current, and clear drops both together.
func TestPublishVersionRoundTrips(t *testing.T) {
	d := newDiagnostics()
	v := 7
	d.setVersion("/w/a.go", []lsp.Diagnostic{diag(1, sevError, "broken")}, &v)

	if got, ok := d.publishedVersion("/w/a.go"); !ok || got != 7 {
		t.Errorf("publishedVersion = (%d, %v), want (7, true)", got, ok)
	}

	d.set("/w/a.go", []lsp.Diagnostic{diag(1, sevError, "broken")})
	if got, ok := d.publishedVersion("/w/a.go"); ok {
		t.Errorf("publishedVersion = (%d, true), want no version", got)
	}

	d.setVersion("/w/a.go", nil, &v)
	if got, ok := d.publishedVersion("/w/a.go"); !ok || got != 7 {
		t.Errorf("publishedVersion after a clean publish = (%d, %v), want (7, true)", got, ok)
	}
	d.clear("/w/a.go")
	if got, ok := d.publishedVersion("/w/a.go"); ok {
		t.Errorf("clear left version %d behind", got)
	}
}

// An empty publish is still a publish, and it still applies to a version. A
// clean file whose version was dropped would compare as fresh forever, which is
// the one reading a per-hunk compile gate must never accept by accident.
func TestEmptyPublishRecordsItsVersion(t *testing.T) {
	d := newDiagnostics()
	v := 3
	d.setVersion("/w/a.go", nil, &v)

	if !d.published("/w/a.go") {
		t.Fatal("an empty publish did not mark the path published")
	}
	if got, ok := d.publishedVersion("/w/a.go"); !ok || got != 3 {
		t.Errorf("publishedVersion = (%d, %v), want (3, true)", got, ok)
	}
}

// The freshness rule is the fix, so every branch is pinned without a server:
// an unpublished path, a server that has not been told about the current text,
// a publish that predates it, a fresh one, and a fresh one whose server sent no
// version at all.
func TestDiagnosticsStatus(t *testing.T) {
	version := func(v int) *int { return &v }
	cases := []struct {
		name       string
		published  bool
		pubVersion *int
		synced     int
		buf        int
		want       string
	}{
		{"unpublished", false, nil, 5, 5, control.LSPStatusUnpublished},
		{"unsynced", true, version(5), 3, 5, control.LSPStatusStale},
		{"version mismatch", true, version(4), 5, 5, control.LSPStatusStale},
		{"fresh", true, version(5), 5, 5, control.LSPStatusOK},
		{"no version fresh", true, nil, 5, 5, control.LSPStatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, detail := diagnosticsStatus(c.published, c.pubVersion, c.synced, c.buf)
			if got != c.want {
				t.Errorf("status = %q, want %q", got, c.want)
			}
			if got == control.LSPStatusOK {
				if detail != "" {
					t.Errorf("an ok status carried detail %q", detail)
				}
				return
			}
			if detail == "" {
				t.Errorf("status %q carried no detail", got)
			}
		})
	}
}

// A caller waiting for a publish is released by the next setVersion for its
// path, and the channel is cleared so a later publish has nobody to notify.
func TestWaitForWakesOnPublish(t *testing.T) {
	d := newDiagnostics()
	got := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		got <- d.waitFor(ctx, "/w/a.go")
	}()

	// The publish must follow the registration, so wait for the waiter to be
	// visible rather than racing the goroutine's start against it.
	limit := time.Now().Add(time.Second)
	for {
		d.mu.Lock()
		n := len(d.waiters["/w/a.go"])
		d.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(limit) {
			t.Fatal("waitFor never registered a waiter")
		}
		time.Sleep(time.Millisecond)
	}

	d.setVersion("/w/a.go", nil, nil)
	if ok := <-got; !ok {
		t.Error("waitFor returned false after a publish")
	}
	d.mu.Lock()
	n := len(d.waiters["/w/a.go"])
	d.mu.Unlock()
	if n != 0 {
		t.Errorf("the publish left %d waiter(s) registered", n)
	}
}

// A wait with no publish gives up when its context is done, and does not leave
// the dead waiter registered for a later publish to close.
func TestWaitForGivesUpOnCancelledContext(t *testing.T) {
	d := newDiagnostics()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if d.waitFor(ctx, "/w/a.go") {
		t.Fatal("waitFor returned true with a cancelled context and no publish")
	}
	d.mu.Lock()
	n := len(d.waiters["/w/a.go"])
	d.mu.Unlock()
	if n != 0 {
		t.Errorf("a cancelled wait left %d waiter(s) registered", n)
	}
}

// The diagnostics caller judges the cache before waiting and recomputes after a
// publish. The transition the control path depends on is an unpublished path
// becoming the reading a matching-version publish carries; a publish for any
// other version stays stale.
func TestReadingRecomputesAfterPublish(t *testing.T) {
	d := newDiagnostics()
	if status, _, _ := d.reading("/w/a.go", 5); status != control.LSPStatusUnpublished {
		t.Fatalf("status before a publish = %q, want %q", status, control.LSPStatusUnpublished)
	}

	v := 5
	d.setVersion("/w/a.go", []lsp.Diagnostic{diag(2, sevError, "boom")}, &v)
	status, detail, items := d.reading("/w/a.go", 5)
	if status != control.LSPStatusOK {
		t.Fatalf("status after the publish = %q, want %q", status, control.LSPStatusOK)
	}
	if detail != "" {
		t.Errorf("an ok reading carried detail %q", detail)
	}
	if len(items) != 1 || items[0].Message != "boom" {
		t.Errorf("items = %v, want the published diagnostic", items)
	}

	v = 6
	d.setVersion("/w/a.go", nil, &v)
	if status, _, _ := d.reading("/w/a.go", 5); status != control.LSPStatusStale {
		t.Errorf("status for a later-version publish = %q, want %q", status, control.LSPStatusStale)
	}
}

// lspCaller.Run is where the diagnostics wait lives, so one call returns the
// reading a publish carries instead of making the driver poll. The publish is
// released after a short pause so the common path — request, wait, wake — is
// the one exercised; a publish that beats the wait is taken by the pre-wait
// read and still returns ok.
func TestDiagnosticsRunReturnsThePublishedReading(t *testing.T) {
	d := newDiagnostics()
	c := lspCaller{
		mode: "diagnostics", status: control.LSPStatusUnpublished,
		store: d, path: "/w/a.go", buf: 5,
	}
	go func() {
		time.Sleep(5 * time.Millisecond)
		v := 5
		d.setVersion("/w/a.go", []lsp.Diagnostic{diag(1, sevError, "boom")}, &v)
	}()

	raw, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out control.LSPResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if out.Status != control.LSPStatusOK {
		t.Fatalf("status = %q, want %q", out.Status, control.LSPStatusOK)
	}
	if len(out.Diags) != 1 || out.Diags[0].Message != "boom" {
		t.Errorf("diags = %v, want the published item", out.Diags)
	}
}

// A versionless publish is not automatically a reading of the current text.
// gopls omits the document version from a publish for a file at version 0,
// which is exactly what its analysis of the on-disk copy looks like, and the
// editor's own unedited buffer is also at version 0. Reading that on-disk set
// as clean is the false ok this pins: the buffer holds a deliberate type error
// the on-disk file does not.
//
// Modelled on TestDiagnosticsRunReturnsThePublishedReading — same caller, same
// bounded wait — with the on-disk publish inserted before the sync.
func TestDiagnosticsRejectsAPreSyncVersionlessPublish(t *testing.T) {
	d := newDiagnostics()
	const path = "/w/broken.go"

	// The server analysed the on-disk file, which is clean, and published with
	// no version: it omits the version at file version 0.
	d.setVersion(path, nil, nil)

	// The buffer now holds a type error at version 2, and the request has told
	// the server about that text.
	d.noteSynced(path)

	if status, detail := d.status(path, 2, 2); status == control.LSPStatusOK {
		t.Fatalf("status = %q (%s); an on-disk publish answered for the buffer", status, detail)
	}

	c := lspCaller{
		mode: "diagnostics", status: control.LSPStatusStale,
		store: d, path: path, buf: 2,
	}
	go func() {
		time.Sleep(5 * time.Millisecond)
		v := 2
		d.setVersion(path, []lsp.Diagnostic{
			diag(1, sevError, "multiple-value s.RevertAuthor(Agent) in single-value context"),
		}, &v)
	}()

	raw, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out control.LSPResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if out.Status != control.LSPStatusOK {
		t.Fatalf("status = %q, want %q", out.Status, control.LSPStatusOK)
	}
	if len(out.Diags) != 1 || !strings.Contains(out.Diags[0].Message, "single-value context") {
		t.Fatalf("diags = %v, want the published type error", out.Diags)
	}
}

// The other half of the rule: a versionless publish that arrives after the sync
// is the server's reading of the text just sent, and a clean one reads clean.
// gopls publishes with no version for a file at version 0, which is exactly the
// unedited buffer.
func TestDiagnosticsReadsAPostSyncVersionlessPublishAsClean(t *testing.T) {
	d := newDiagnostics()
	const path = "/w/clean.go"
	d.noteSynced(path)
	d.setVersion(path, nil, nil) // a publish for the text the sync just sent

	if status, detail := d.status(path, 0, 0); status != control.LSPStatusOK {
		t.Fatalf("status = %q (%s), want %q", status, detail, control.LSPStatusOK)
	}
}

// With no publish after the sync, the answer is honest rather than clean: the
// caller's bounded wait gives up and reports the non-ok status, not an empty ok.
func TestDiagnosticsWithoutAPostSyncPublishStaysStale(t *testing.T) {
	d := newDiagnostics()
	const path = "/w/quiet.go"
	d.setVersion(path, nil, nil) // the on-disk clean set, before the sync
	d.noteSynced(path)

	c := lspCaller{
		mode: "diagnostics", status: control.LSPStatusStale,
		store: d, path: path, buf: 1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	raw, err := c.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out control.LSPResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if out.Status == control.LSPStatusOK {
		t.Fatalf("status = ok with no publish for the current text")
	}
	if len(out.Diags) != 0 {
		t.Errorf("diags = %v, want none from a stale reading", out.Diags)
	}
}

// Even a versionless publish that arrives after the sync is not a reading of an
// edited buffer: gopls omits the version only at file version 0, so a
// versionless set for a buffer with edits cannot be the text just sent. This
// is the post-sync half of the on-disk race, where the sequence rule alone
// would accept the publish because it landed after the sync.
func TestDiagnosticsRejectsAPostSyncVersionlessPublishForAnEditedBuffer(t *testing.T) {
	d := newDiagnostics()
	const path = "/w/edited.go"
	d.noteSynced(path)
	// The on-disk clean set arrives after the sync, with no version.
	d.setVersion(path, nil, nil)

	if status, detail := d.status(path, 2, 2); status == control.LSPStatusOK {
		t.Fatalf("status = %q (%s); a versionless on-disk publish answered for the edited buffer", status, detail)
	}
}

// The sequence rule on its own, with the buffer unedited so the version rule
// cannot be what rejects the publish: an on-disk set that predates the sync is
// stale even at version 0.
func TestDiagnosticsRejectsAVersionlessPublishThatPredatesTheSync(t *testing.T) {
	d := newDiagnostics()
	const path = "/w/unchanged.go"
	d.setVersion(path, nil, nil) // the on-disk clean set, before the sync
	d.noteSynced(path)

	if status, detail := d.status(path, 0, 0); status == control.LSPStatusOK {
		t.Fatalf("status = %q (%s); a pre-sync on-disk publish answered as current", status, detail)
	}
}
