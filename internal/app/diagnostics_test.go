package app

import (
	"strings"
	"testing"

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
