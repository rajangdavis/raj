package intent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPublishCheckBranchRefusesBaseAndProtected pins the hard rules: the
// branch may not be the base (however the base is spelled), a protected trunk
// name, or an unsafe ref name.
func TestPublishCheckBranchRefusesBaseAndProtected(t *testing.T) {
	cases := []struct {
		branch, base string
		wantErr      bool
	}{
		{"raj/wave-task-1", "main", false},
		{"raj/wave-task-1", "origin/main", false},
		{"main", "main", true},
		{"main", "origin/main", true},
		{"master", "origin/main", true},
		{"-bad", "main", true},
		{"raj/wave x", "main", true},
		{"", "main", true},
	}
	for _, c := range cases {
		err := CheckBranch(c.branch, c.base)
		if (err != nil) != c.wantErr {
			t.Errorf("CheckBranch(%q, %q) = %v, wantErr %v", c.branch, c.base, err, c.wantErr)
		}
	}
}

// TestPublishBranchForWave pins the one-branch naming: raj/wave-<slug>.
func TestPublishBranchForWave(t *testing.T) {
	if got := BranchForWave("H5: publish as proposal"); got != "raj/wave-H5-publish-as-proposal" {
		t.Errorf("BranchForWave = %q", got)
	}
	if got := BranchForWave("task_1"); got != "raj/wave-task_1" {
		t.Errorf("BranchForWave underscore = %q", got)
	}
	if got := BranchForWave(""); got != "raj/wave-wave" {
		t.Errorf("BranchForWave empty = %q", got)
	}
}

// TestPublishHashFile pins the content hash: an edit changes it.
func TestPublishHashFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(p, []byte("echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h1, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == "" {
		t.Fatal("empty hash")
	}
	if err := os.WriteFile(p, []byte("echo bye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h2, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Error("the hash did not change with the content")
	}
}
