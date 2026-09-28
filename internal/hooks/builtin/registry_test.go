package builtin

import (
	"context"
	"strings"
	"testing"
)

// testLeaf is a trivial Leaf for the registry tests.
type testLeaf struct{ name string }

func (l testLeaf) Name() string { return l.name }

func (l testLeaf) Run(ctx context.Context, args map[string]any) (Result, error) {
	return Result{Output: l.name}, nil
}

// TestRegisterAndLookup pins the happy path: a registered name is found with
// the policy it declared, and a name that was never registered is not.
func TestRegisterAndLookup(t *testing.T) {
	if err := Register("registrytest.one", testLeaf{name: "registrytest.one"}, AgentDefault); err != nil {
		t.Fatalf("Register: %v", err)
	}
	leaf, policy, ok := Lookup("registrytest.one")
	if !ok || leaf == nil || leaf.Name() != "registrytest.one" || policy != AgentDefault {
		t.Fatalf("Lookup = %v, %v, %v; want the registered leaf and its policy", leaf, policy, ok)
	}
	if _, _, ok := Lookup("registrytest.absent"); ok {
		t.Fatalf("Lookup of an unregistered name reported a leaf")
	}
}

// TestRegisterRefusesDuplicate pins the one-name-one-implementation rule: a
// second Register of the same name is refused and does not replace the first.
func TestRegisterRefusesDuplicate(t *testing.T) {
	first := testLeaf{name: "registrytest.dup"}
	if err := Register("registrytest.dup", first, AgentDefault); err != nil {
		t.Fatalf("Register: %v", err)
	}
	err := Register("registrytest.dup", testLeaf{name: "registrytest.dup"}, AgentDefault)
	if err == nil {
		t.Fatalf("duplicate Register was accepted")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate Register error = %q; want it to name the clash", err)
	}
	if leaf, _, ok := Lookup("registrytest.dup"); !ok || leaf != Leaf(first) {
		t.Fatalf("Lookup after a refused duplicate = %v, %v; want the first leaf", leaf, ok)
	}
}

// TestRegisterRefusesEmptyAndNil pins the two bad inputs: an empty name and a
// nil leaf are both refused before the map is touched.
func TestRegisterRefusesEmptyAndNil(t *testing.T) {
	if err := Register("", testLeaf{name: "x"}, AgentDefault); err == nil {
		t.Errorf("Register with an empty name was accepted")
	}
	if err := Register("registrytest.nil", nil, AgentDefault); err == nil {
		t.Errorf("Register with a nil leaf was accepted")
	}
}

// TestRegisterRefusesNameMismatch pins that the registry key, not the leaf, is
// the authority: a leaf reporting a different name would be reachable under one
// name and answer as another, so it is refused before it is stored.
func TestRegisterRefusesNameMismatch(t *testing.T) {
	err := Register("registrytest.key", testLeaf{name: "registrytest.other"}, AgentDefault)
	if err == nil {
		t.Fatalf("Register accepted a leaf whose Name does not match")
	}
	if !strings.Contains(err.Error(), "registrytest.other") {
		t.Fatalf("mismatch Register error = %q; want it to name the leaf's own name", err)
	}
	if _, _, ok := Lookup("registrytest.key"); ok {
		t.Fatalf("a refused mismatch was still registered")
	}
}
