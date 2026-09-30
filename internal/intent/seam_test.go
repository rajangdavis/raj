package intent

import (
	"strings"
	"testing"
)

func proveSet(name string) Set {
	return Set{name: {Name: name, Base: "main"}}
}

func TestProvePasses(t *testing.T) {
	set := proveSet("wave")
	materialised := false
	proof, err := ProveNamed(set, "wave", func(in Intention) (string, func(), error) {
		materialised = true
		return t.TempDir(), nil, nil
	}, func(dir string) (int, string, error) {
		return 0, "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !materialised {
		t.Fatal("the intention was not materialised alone")
	}
	if proof.Check != "pass" {
		t.Fatalf("got %s, want pass (%s)", proof.Check, proof.Error)
	}
}

func TestProveReportsFailingTail(t *testing.T) {
	set := proveSet("wave")
	proof, err := ProveNamed(set, "wave", func(in Intention) (string, func(), error) {
		return t.TempDir(), nil, nil
	}, func(dir string) (int, string, error) {
		return 1, "line one\n# raj/internal/intent\n--- FAIL: TestX\nboom", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if proof.Check != "fail" {
		t.Fatalf("got %s, want fail", proof.Check)
	}
	if !strings.Contains(proof.Error, "exit 1") || !strings.Contains(proof.Error, "boom") {
		t.Fatalf("failing tail not reported: %q", proof.Error)
	}
}

func TestProveUnknownIntentionRefused(t *testing.T) {
	if _, err := ProveNamed(Set{}, "missing", nil, nil); err == nil {
		t.Fatal("proving an unknown intention must be refused")
	}
}
