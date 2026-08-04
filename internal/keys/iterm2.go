package keys

import (
	"fmt"
	"sort"
	"strings"
)

// iTerm2 has no conditional binding like Ghostty's kkp_on gate: a key mapping
// applies whenever the profile is in use. The scoping mechanism is the profile
// itself — run raj under a profile that forwards these chords, and every other
// iTerm2 window keeps cmd+w closing tabs.
//
// This is weaker than the gate in one specific way: while raj is running, that
// window cannot close its own tab with cmd+w. Ghostty's gate releases the chord
// the moment raj exits; a profile does not know raj is gone.
//
// NSEvent modifier flags, which iTerm2 uses as the mask in a mapping key.
const (
	itermShift = 0x20000
	itermCtrl  = 0x40000
	itermAlt   = 0x80000
	itermCmd   = 0x100000
)

// itermKeys maps raj's key names to the character iTerm2 matches on, which is
// NSEvent's charactersIgnoringModifiers. Arrows and friends are in the private
// use area, where AppKit puts them.
var itermKeys = map[string]rune{
	"up": 0xF700, "down": 0xF701, "left": 0xF702, "right": 0xF703,
	"home": 0xF729, "end": 0xF72B, "pgup": 0xF72C, "pgdown": 0xF72D,
	"enter": 0x0D, "tab": 0x09, "esc": 0x1B, "backspace": 0x7F, "space": 0x20,
}

// ITerm2Profile renders a dynamic profile that forwards raj's chords.
//
// Drop it in ~/Library/Application Support/iTerm2/DynamicProfiles/ and iTerm2
// picks it up without a restart. Verify it with cmd/keyprobe -checklist: the
// mapping-key format has varied across iTerm2 versions, and measuring is the
// only way to know which one this build wants.
func ITerm2Profile(name string) string {
	if name == "" {
		name = "raj"
	}
	type entry struct{ key, seq string }
	var entries []entry

	for _, b := range Bindings {
		key, ok := itermMappingKey(b.Chord)
		if !ok {
			continue
		}
		// Action 10 is "Send Escape Sequence": iTerm2 prepends the ESC itself,
		// so Text is the sequence without it.
		entries = append(entries, entry{key, "[" + b.Seq})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })

	var sb strings.Builder
	sb.WriteString("{\n  \"Profiles\": [\n    {\n")
	sb.WriteString("      \"Name\": \"" + name + "\",\n")
	sb.WriteString("      \"Guid\": \"raj-generated-profile\",\n")
	sb.WriteString("      \"Dynamic Profile Parent Name\": \"Default\",\n")
	sb.WriteString("      \"Keyboard Map\": {\n")
	for i, e := range entries {
		comma := ","
		if i == len(entries)-1 {
			comma = ""
		}
		sb.WriteString(fmt.Sprintf("        %q: { \"Action\": 10, \"Text\": %q }%s\n",
			e.key, e.seq, comma))
	}
	sb.WriteString("      }\n    }\n  ]\n}\n")
	return sb.String()
}

// itermMappingKey builds iTerm2's "0xCHAR-0xMASK" mapping key from a chord.
func itermMappingKey(chord string) (string, bool) {
	parts := strings.Split(chord, "+")
	mask := 0
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "shift":
			mask |= itermShift
		case "ctrl":
			mask |= itermCtrl
		case "alt":
			mask |= itermAlt
		case "super":
			mask |= itermCmd
		default:
			return "", false
		}
	}
	name := parts[len(parts)-1]
	r, ok := itermKeys[name]
	if !ok {
		runes := []rune(name)
		if len(runes) != 1 {
			return "", false
		}
		r = runes[0]
		// AppKit's charactersIgnoringModifiers strips every modifier EXCEPT
		// shift, so shift+L reports "L" rather than "l". Generating the
		// lowercase code makes every shift+letter mapping silently miss —
		// which is why cmd+shift+L did nothing while cmd+D worked.
		if mask&itermShift != 0 && r >= 'a' && r <= 'z' {
			r = r - 'a' + 'A'
		}
	}
	if mask == 0 {
		return "", false // unmodified keys need no mapping
	}
	return fmt.Sprintf("0x%x-0x%x", r, mask), true
}
