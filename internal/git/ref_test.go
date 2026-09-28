package git

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recordingRunner captures the commands a Service runs, so a ref-write test can
// assert what git would have been asked to do without running it.
type recordingRunner struct {
	calls []recordedCall
}

type recordedCall struct {
	args  []string
	stdin string
}

func (r *recordingRunner) Run(_ context.Context, _ string, stdin []byte, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedCall{args: append([]string(nil), args...), stdin: string(stdin)})
	return nil, nil
}

// UpdateRefCAS refuses any ref but the allowlisted baseline, before git runs:
// the one ref a landed wave may move cannot become a general ref-write verb.
func TestUpdateRefCASRefusesOtherRefs(t *testing.T) {
	t.Parallel()
	r := &recordingRunner{}
	svc := NewWithRunner("/w", r)
	err := svc.UpdateRefCAS(context.Background(), "refs/heads/main", "abc", "")
	if !errors.Is(err, ErrRefNotAllowed) {
		t.Fatalf("UpdateRefCAS(other ref) = %v, want ErrRefNotAllowed", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("the refusal ran git %d time(s); it must refuse before starting git", len(r.calls))
	}
}

// UpdateRefCAS sends a create when there is no old value and a compare-and-swap
// update when there is, through update-ref --stdin so the object format does
// not matter.
func TestUpdateRefCASWriteForm(t *testing.T) {
	t.Parallel()
	r := &recordingRunner{}
	svc := NewWithRunner("/w", r)

	if err := svc.UpdateRefCAS(context.Background(), BaselineRef, "new1", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := strings.Join(r.calls[0].args, " "); got != "update-ref --stdin" {
		t.Errorf("create args = %q, want update-ref --stdin", got)
	}
	if got := r.calls[0].stdin; got != "create "+BaselineRef+" new1\n" {
		t.Errorf("create stdin = %q", got)
	}

	if err := svc.UpdateRefCAS(context.Background(), BaselineRef, "new2", "old2"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := r.calls[1].stdin; got != "update "+BaselineRef+" new2 old2\n" {
		t.Errorf("update stdin = %q", got)
	}
}

// UpdateRefCAS creates and then moves raj/baseline, refuses a stale old value,
// and leaves HEAD alone.
func TestUpdateRefCASMovesOnlyBaseline(t *testing.T) {
	dir := initRepo(t)
	svc := New(dir)
	ctx := context.Background()
	before, err := svc.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tree := runGit(t, dir, "rev-parse", "HEAD^{tree}")
	first, err := svc.CommitTree(ctx, tree, []string{before}, "first landing")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateRefCAS(ctx, BaselineRef, first, ""); err != nil {
		t.Fatalf("create baseline: %v", err)
	}
	if got, err := svc.RevParse(ctx, BaselineRef); err != nil || got != first {
		t.Fatalf("baseline = %q, %v; want %q", got, err, first)
	}

	second, err := svc.CommitTree(ctx, tree, []string{first}, "second landing")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateRefCAS(ctx, BaselineRef, second, first); err != nil {
		t.Fatalf("move baseline: %v", err)
	}
	if got, err := svc.RevParse(ctx, BaselineRef); err != nil || got != second {
		t.Fatalf("baseline = %q, %v; want the moved %q", got, err, second)
	}

	// A compare-and-swap with the wrong old value is refused, and the ref
	// stays where it was.
	stale := strings.Repeat("0", 40)
	if err := svc.UpdateRefCAS(ctx, BaselineRef, first, stale); err == nil {
		t.Fatal("UpdateRefCAS with a stale old value succeeded, want a refusal")
	}
	if got, _ := svc.RevParse(ctx, BaselineRef); got != second {
		t.Fatalf("baseline moved despite a refused CAS: %q, want %q", got, second)
	}
	if got, _ := svc.RevParse(ctx, "HEAD"); got != before {
		t.Fatalf("HEAD moved to %q, want %q", got, before)
	}
}
