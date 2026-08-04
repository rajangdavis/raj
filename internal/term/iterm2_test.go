package term

import (
	"os"
	"strings"
	"testing"
)

func withEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		old, had := os.LookupEnv(k)
		// Always Setenv, never Unsetenv: an empty override is meaningful here
		// — it disables switching — and unsetting would make it indistinguish-
		// able from "not configured", which defaults to the raj profile.
		os.Setenv(k, v)
		t.Cleanup(func() {
			if had {
				os.Setenv(k, old)
			} else {
				os.Unsetenv(k)
			}
		})
	}
}

func TestProfileSwitchOnlyUnderITerm2(t *testing.T) {
	withEnv(t, map[string]string{"TERM_PROGRAM": "ghostty", "ITERM_PROFILE": "Default"})
	if newProfileSwitch().active {
		t.Error("switched profiles outside iTerm2")
	}
}

// Without a restore target raj must not switch: leaving the window on raj's
// profile after exit breaks cmd+w with no obvious way back.
func TestProfileSwitchNeedsARestoreTarget(t *testing.T) {
	withEnv(t, map[string]string{"TERM_PROGRAM": "iTerm.app", "ITERM_PROFILE": ""})
	if newProfileSwitch().active {
		t.Error("switched without knowing what to restore")
	}
}

// Already being on the raj profile means there is nothing to do.
func TestProfileSwitchNoopWhenAlreadyThere(t *testing.T) {
	withEnv(t, map[string]string{"TERM_PROGRAM": "iTerm.app", "ITERM_PROFILE": "raj"})
	if newProfileSwitch().active {
		t.Error("switched to the profile already in use")
	}
}

func TestProfileSwitchRoundTrip(t *testing.T) {
	withEnv(t, map[string]string{"TERM_PROGRAM": "iTerm.app", "ITERM_PROFILE": "Verdant"})
	p := newProfileSwitch()
	if !p.active {
		t.Fatal("expected switching to be active")
	}
	var in, out strings.Builder
	p.enter(&in)
	p.leave(&out)
	if got := in.String(); got != "\x1b]1337;SetProfile=raj\x07" {
		t.Errorf("enter sent %q", got)
	}
	if got := out.String(); got != "\x1b]1337;SetProfile=Verdant\x07" {
		t.Errorf("leave sent %q, want the original profile", got)
	}
}

// An empty override disables switching, for people who put the mappings in
// their everyday profile.
func TestProfileSwitchDisabledByEnv(t *testing.T) {
	withEnv(t, map[string]string{
		"TERM_PROGRAM": "iTerm.app", "ITERM_PROFILE": "Verdant", EnvProfile: "",
	})
	if newProfileSwitch().active {
		t.Error("an empty override should disable switching")
	}
}

func TestProfileSwitchCustomName(t *testing.T) {
	withEnv(t, map[string]string{
		"TERM_PROGRAM": "iTerm.app", "ITERM_PROFILE": "Verdant", EnvProfile: "editing",
	})
	var in strings.Builder
	p := newProfileSwitch()
	p.enter(&in)
	if !strings.Contains(in.String(), "SetProfile=editing") {
		t.Errorf("enter sent %q", in.String())
	}
}
