package intent

import (
	"fmt"
	"strings"
)

// Set is the intentions available to resolve, keyed by name.
type Set map[string]Intention

// Chain walks name's base chain and returns it in dependency order, deepest
// base first and name last. A base that names another intention in the set is
// followed; a base the set does not hold is a git ref and ends the chain. A
// cycle is refused naming the intentions on it. A base intention that has been
// deleted is not silently re-anchored: it reads as a ref and is refused by name
// when the base is resolved against git.
func (s Set) Chain(name string) ([]Intention, error) {
	var chain []Intention
	seen := make(map[string]bool)
	cur := name
	for cur != "" {
		if seen[cur] {
			names := make([]string, 0, len(chain)+1)
			for _, in := range chain {
				names = append(names, in.Name)
			}
			names = append(names, cur)
			return nil, fmt.Errorf("intent: base cycle: %s", strings.Join(names, " -> "))
		}
		seen[cur] = true
		in, ok := s[cur]
		if !ok {
			break // a git ref base: the chain ends here
		}
		chain = append([]Intention{in}, chain...)
		cur = in.Base
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("intent: no such intention %q", name)
	}
	return chain, nil
}

// Member is one membership entry: a group id qualified by the
// workspace-relative path of the buffer that numbers it. The id alone is
// session-local, so two buffers can both number a group 1; the pair is what
// makes membership unambiguous across buffers.
type Member struct {
	ID   uint64 `json:"id"`
	Path string `json:"path"`
}

// ResolveMembers resolves in's qualified members through find, returned in
// membership order. find maps a member to the live group at its path; a member
// it does not know is a hard error naming the intention, the missing group and
// the dependent groups of the selection. Membership is explicit and nothing
// else is added, so a missing group cannot be papered over with the groups
// around it (D2). A member with no path is a legacy row the finder resolves by
// bare id when exactly one live buffer numbers it.
func (in Intention) ResolveMembers(find func(Member) (Member, bool)) ([]Member, error) {
	out := make([]Member, 0, len(in.Members))
	for _, want := range in.Members {
		m, ok := find(want)
		if !ok {
			return nil, missingMemberError(in, want)
		}
		out = append(out, m)
	}
	return out, nil
}

// missingMemberError names both sides of the dependency: the group the live
// buffers do not hold, and the other groups of the selection that cannot be
// composed without it (D2). The missing member's path is named when it has one,
// so a collision across buffers identifies the buffer it came from.
func missingMemberError(in Intention, missing Member) error {
	dependent := make([]Member, 0, len(in.Members))
	for _, m := range in.Members {
		if m != missing {
			dependent = append(dependent, m)
		}
	}
	if missing.Path == "" {
		return fmt.Errorf("intent: intention %q: dependent group(s) %v need member group %d, which is missing",
			in.Name, dependent, missing.ID)
	}
	return fmt.Errorf("intent: intention %q: dependent group(s) %v need member group %d in %s, which is missing",
		in.Name, dependent, missing.ID, missing.Path)
}
