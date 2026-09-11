package app

import (
	"strings"
	"testing"

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
