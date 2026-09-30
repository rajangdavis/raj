package intent

import (
	"fmt"
	"strings"
)

// Seam proof. A named intention is a seam: a selection of change sets over a
// base that will be published as one body of work. Whether it stands alone is
// not a graph question -- reconciliation already answers "can these sit
// together" -- but a build question. Prove materialises the intention alone
// over its base and runs the workspace's check hook on that tree.
//
// A failing proof IS the detector: a member needed a change the seam excludes,
// or a build unit got split. The caller reports the failing output tail and the
// user fixes the selection. Nothing is written: no intention, no ref, no index.

// Proof is one named intention's seam proof: whether it, materialised alone
// over its base, passed the workspace's check hook. Check is "pass" or "fail";
// Error carries the failing exit and the output tail.
type Proof struct {
	Name  string `json:"name"`
	Check string `json:"check"`
	Error string `json:"error,omitempty"`
}

// Materialiser materialises the intention alone into a scratch tree and
// returns its directory with a cleanup. It is injected so Prove is testable
// without git.
type Materialiser func(in Intention) (dir string, cleanup func(), err error)

// Checker runs the workspace's check hook in dir and returns its exit code and
// combined output. A non-zero exit is data, not an error; err is only for a run
// that could not start.
type Checker func(dir string) (code int, output string, err error)

// Prove materialises in alone over its base and runs check over the tree. It
// reports pass or, on failure, the exit and the last lines of the hook's
// output. A materialise or start failure is reported the same way, as a failing
// proof, so the caller always gets a verdict.
func Prove(in Intention, materialise Materialiser, check Checker) Proof {
	proof := Proof{Name: in.Name, Check: "fail"}
	dir, cleanup, err := materialise(in)
	if err != nil {
		proof.Error = err.Error()
		return proof
	}
	if cleanup != nil {
		defer cleanup()
	}
	code, out, err := check(dir)
	if err != nil {
		proof.Error = err.Error()
		return proof
	}
	if code == 0 {
		proof.Check = "pass"
		return proof
	}
	proof.Error = fmt.Sprintf("exit %d: %s", code, tailLines(out, 20))
	return proof
}

// ProveNamed resolves name in set and proves it. A missing name is refused by
// name, so a typo cannot read as a passing proof of nothing.
func ProveNamed(set Set, name string, materialise Materialiser, check Checker) (Proof, error) {
	in, ok := set[name]
	if !ok {
		return Proof{}, fmt.Errorf("intent prove: no such intention %q", name)
	}
	return Prove(in, materialise, check), nil
}

// tailLines keeps the last n lines of a proof's output.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
