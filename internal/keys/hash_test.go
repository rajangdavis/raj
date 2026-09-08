package keys

import (
	"encoding/json"
	"strings"
	"testing"
)

// The stamp is only useful if it moves when the table moves, so this is the
// property worth asserting: same table, same hash; changed table, changed hash.
func TestHashTracksTheTable(t *testing.T) {
	before := Hash()
	if again := Hash(); again != before {
		t.Fatalf("Hash is not stable: %q then %q", before, again)
	}

	restore := Bindings
	defer func() { Bindings = restore }()

	// Every field reaches a config or a comment in one, so every field moves
	// the hash. A change that does not move it is a change raj cannot warn
	// about.
	for _, mutate := range []struct {
		name string
		fn   func(b *Binding)
	}{
		{"seq", func(b *Binding) { b.Seq = "999;9u" }},
		{"mac trigger", func(b *Binding) { b.Mac = "cmd+q" }},
		{"linux trigger", func(b *Binding) { b.Linux = "ctrl+q" }},
		{"chord", func(b *Binding) { b.Chord = "super+q" }},
		{"note", func(b *Binding) { b.Note = "changed" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			Bindings = append([]Binding{}, restore...)
			mutate.fn(&Bindings[0])
			if got := Hash(); got == before {
				t.Errorf("changing %s left the hash at %q", mutate.name, got)
			}
		})
	}

	Bindings = append([]Binding{}, restore...)
	if got := Hash(); got != before {
		t.Errorf("restoring the table gave %q, want %q", got, before)
	}
}

// Both emitters must carry a stamp raj can read back, or the startup check has
// nothing to compare against.
func TestEmittersStampTheHash(t *testing.T) {
	for _, tc := range []struct{ name, out string }{
		{"ghostty macos", GhosttyConfig("macos")},
		{"ghostty linux", GhosttyConfig("linux")},
		{"iterm2", ITerm2Profile("raj")},
	} {
		if got := InstalledHash(tc.out); got != Hash() {
			t.Errorf("%s: stamp is %q, want %q", tc.name, got, Hash())
		}
	}
}

// The stamp rides on a JSON string, so the profile has to still parse. This is
// the guard on injecting it by string concatenation.
func TestITerm2ProfileIsValidJSON(t *testing.T) {
	var v struct {
		Profiles []struct {
			Description string         `json:"Description"`
			KeyboardMap map[string]any `json:"Keyboard Map"`
			Name        string         `json:"Name"`
		} `json:"Profiles"`
	}
	if err := json.Unmarshal([]byte(ITerm2Profile("raj")), &v); err != nil {
		t.Fatalf("profile does not parse: %v", err)
	}
	if len(v.Profiles) != 1 {
		t.Fatalf("got %d profiles, want 1", len(v.Profiles))
	}
	if !strings.Contains(v.Profiles[0].Description, Hash()) {
		t.Errorf("Description %q does not carry the hash", v.Profiles[0].Description)
	}
	if len(v.Profiles[0].KeyboardMap) == 0 {
		t.Error("Keyboard Map is empty")
	}
}

// An unstamped file is not a stale file: raj cannot tell a hand-written config
// from one generated before stamping existed, so it must report neither.
func TestInstalledHashOnUnstampedInput(t *testing.T) {
	for _, in := range []string{"", "# just a config\nkeybind = a=b\n", "{}"} {
		if got := InstalledHash(in); got != "" {
			t.Errorf("InstalledHash(%q) = %q, want empty", in, got)
		}
	}
}

// The stamp is read out of arbitrary surrounding text, so it must stop at the
// end of the hex rather than running into whatever follows.
func TestInstalledHashStopsAtNonHex(t *testing.T) {
	if got := InstalledHash("# " + HashMarker + "abc123\nkeybind = x\n"); got != "abc123" {
		t.Errorf("got %q, want abc123", got)
	}
	if got := InstalledHash(`"` + HashMarker + `deadbeef",`); got != "deadbeef" {
		t.Errorf("got %q, want deadbeef", got)
	}
}
