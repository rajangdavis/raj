#!/bin/sh
# raj-guard.test.sh — the guard's save and spawn rules, one case each.
#
# The guard is pure: hook JSON on stdin, a decision on stdout, no state. Every
# case pipes a sample through the real script and reads the decision back, so a
# change that flips an allow to a deny (or the reverse) fails the run. The exit
# is non-zero on any mismatch, so `make check` stops on it.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
guard=$here/raj-guard.sh

fails=0

# decide prints the guard's decision for a payload: deny, or allow when the
# guard emits nothing. Extra arguments are NAME=VALUE assignments for env, so
# the escape case can set RAJ_SPAWN_ANY for the one call.
decide() {
  payload=$1
  shift
  out=$(printf '%s' "$payload" | env "$@" sh "$guard" 2>/dev/null || true)
  if [ -z "$out" ]; then
    printf 'allow'
  else
    printf '%s' "$out" | jq -r '.hookSpecificOutput.permissionDecision // "allow"'
  fi
}

expect() {
  desc=$1
  want=$2
  got=$3
  if [ "$got" != "$want" ]; then
    printf 'raj-guard.test: %s: want %s, got %s\n' "$desc" "$want" "$got" >&2
    fails=$((fails + 1))
  fi
}

# save: denied at a command position, allowed when only mentioned.
expect "save is denied" deny "$(decide '{"tool_name":"Bash","tool_input":{"command":"raj ctl save main.go"}}')"
expect "save mention is allowed" allow "$(decide '{"tool_name":"Bash","tool_input":{"command":"echo raj ctl save"}}')"

# spawn: an allowed type passes, anything else (including unspecified) is denied.
expect "raj:raj spawn allowed" allow "$(decide '{"tool_name":"Agent","tool_input":{"subagent_type":"raj:raj"}}')"
expect "bare review spawn allowed" allow "$(decide '{"tool_name":"Task","tool_input":{"subagent_type":"review"}}')"
expect "general-purpose spawn denied" deny "$(decide '{"tool_name":"Agent","tool_input":{"subagent_type":"general-purpose"}}')"
expect "unspecified spawn denied" deny "$(decide '{"tool_name":"Agent","tool_input":{}}')"

# the user's escape: RAJ_SPAWN_ANY=1 allows any type.
expect "escape allows any spawn" allow "$(decide '{"tool_name":"Agent","tool_input":{"subagent_type":"general-purpose"}}' RAJ_SPAWN_ANY=1)"

if [ "$fails" -ne 0 ]; then
  printf 'raj-guard.test: %d case(s) failed\n' "$fails" >&2
  exit 1
fi
printf 'raj-guard.test: ok\n'
