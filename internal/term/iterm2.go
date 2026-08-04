package term

import (
	"fmt"
	"io"
	"os"
)

// iTerm2 has no conditional keybinding: a profile's key mappings apply whenever
// that profile is in use, so installing them globally would break cmd+w for
// every shell in that window. Ghostty solves this with the kkp_on gate, which
// releases the chords the moment raj stops asking for them.
//
// The equivalent here has to be driven from raj's side. iTerm2 lets a session
// change its own profile with OSC 1337 SetProfile, so raj switches to the raj
// profile on entry and back on exit — including on suspend, on a fatal signal,
// and on panic, since all of those already route through Leave.
//
// It is weaker than the gate in one way worth knowing: if raj is killed with
// SIGKILL, nothing runs and the window keeps the raj profile until you switch
// it back by hand.
type profileSwitch struct {
	to      string
	restore string
	active  bool
}

// EnvProfile names the iTerm2 profile raj switches to. Empty disables
// switching, which is what you want if you installed the mappings into your
// everyday profile instead.
const EnvProfile = "RAJ_ITERM_PROFILE"

// DefaultITermProfile matches the name `raj --config iterm2` generates.
const DefaultITermProfile = "raj"

// newProfileSwitch decides whether to switch profiles at all.
//
// It only acts under iTerm2, and only when it knows what to restore: switching
// to raj's profile without being able to switch back would leave the window
// with cmd+w broken after raj exits, which is worse than not switching.
func newProfileSwitch() profileSwitch {
	if os.Getenv("TERM_PROGRAM") != "iTerm.app" {
		return profileSwitch{}
	}
	to, ok := os.LookupEnv(EnvProfile)
	if !ok {
		to = DefaultITermProfile
	}
	if to == "" {
		return profileSwitch{}
	}
	// iTerm2 exports the session's profile name, so the restore target needs no
	// query round-trip. A session started before the variable existed, or one
	// whose profile changed since, falls back to Default.
	restore := os.Getenv("ITERM_PROFILE")
	if restore == "" || restore == to {
		return profileSwitch{}
	}
	return profileSwitch{to: to, restore: restore, active: true}
}

func (p *profileSwitch) enter(w io.Writer) {
	if p.active {
		fmt.Fprint(w, setProfile(p.to))
	}
}

func (p *profileSwitch) leave(w io.Writer) {
	if p.active {
		fmt.Fprint(w, setProfile(p.restore))
	}
}

// setProfile is iTerm2's proprietary profile-switch sequence. Terminals that do
// not understand OSC 1337 ignore it, so sending it is safe even when the
// detection above is wrong.
func setProfile(name string) string {
	return "\x1b]1337;SetProfile=" + name + "\x07"
}
