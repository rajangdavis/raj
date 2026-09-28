#!/bin/sh
# raj-guard.sh — deny `raj ctl save` from the agent.
#
# Saving is the user's gesture and never the agent's: `raj ctl save` writes the
# user's buffers to disk, so an agent that runs it turns the user's review into
# a formality. This is the client-side half of the law; raj itself also refuses
# a save from anything but the human's own connection, so this only makes the
# refusal arrive earlier and clearer.
#
# Claude Code PreToolUse contract: JSON on stdin, JSON decision on stdout.
# A `deny` blocks the tool call and hands the reason to the model.
#
# The regex mirrors plugins/raj-gate.ts: the binary by basename so an absolute
# path or env prefix still trips it, and nothing between `raj ctl` and `save`.
set -eu

# jq is present in the image; if it ever is not, fail safe rather than allow.
if ! command -v jq >/dev/null 2>&1; then
  printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"raj guard: jq missing, refusing to run tools it cannot inspect"}}'
  exit 0
fi

cmd=$(jq -r '.tool_input.command // empty')
case "$cmd" in
  *"raj ctl save"*|*"raj  ctl save"*)
    # Word-boundary-ish check mirroring RAJ_SAVE, without a PCRE dependency.
    # `case` cannot express (?:^|[^\w-]) cleanly, so reject known-safe prefixes.
    ;;
  *) exit 0 ;;
esac

# Do not trip on `raj ctl save` appearing inside a larger safe word, e.g.
# `echo raj ctl save`. The pattern above is deliberately conservative; the
# authority is the server, which refuses a non-human save regardless.
printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"blocked: `raj ctl save` is the user\x27s own action, and an agent must never save for them. Ask the user for explicit permission and let them save; do not retry this call."}}'
exit 0
