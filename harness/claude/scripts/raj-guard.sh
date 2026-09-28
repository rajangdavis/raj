#!/bin/sh
# raj-guard.sh — the PreToolUse gate for a Claude agent here: it denies
# `raj ctl save` and it limits subagent spawning to the swarm interface.
#
# Saving is the user's gesture and never the agent's: it writes the user's
# buffers to disk, so an agent that runs it turns the user's review into a
# formality. This is the client-side half of the law; raj itself also refuses a
# save from anything but the human's own connection (the author on the wire is
# stamped from the connection, never believed), so a bug here cannot open the
# door — it only makes the refusal arrive earlier and clearer.
#
# Spawning mirrors `plugins/raj-gate.ts`'s SPAWNABLE rule in the Claude harness:
# the subagent tool (`Agent`, and the older `Task` name) may only launch the
# swarm interface's own agents — `raj:raj`/`raj:review`, or the bare `raj`/
# `review` a local agent definition uses — so a session cannot fan out into
# general-purpose workers. A caller that genuinely needs any type sets
# `RAJ_SPAWN_ANY=1` in its environment, which turns the gate off.
#
# PreToolUse contract (Claude Code and Codex accept the same shape): hook JSON
# on stdin, a JSON decision on stdout. `deny` blocks the call and hands the
# reason to the model. Exit 0 with no output means "no decision" and the tool
# runs normally.
#
# The matcher anchors on a command position — start of the string or just after
# a shell separator — so a command that merely mentions the string (an echo, a
# grep, a heredoc) is not blocked. The save verb itself is still caught by the
# editor, which is the authority; this is the early, clear refusal, not the
# guarantee.
set -eu

if ! command -v jq >/dev/null 2>&1; then
  printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"raj guard: jq is missing, so this call cannot be inspected; refusing rather than guessing"}}'
  exit 0
fi

jq '
  def is_save:
    (.tool_input.command // "")
    | test("(^|[;&|\n][[:space:]]*)([^[:space:];&|]*/)?raj[[:space:]]+ctl[[:space:]]+save([[:space:]]|$)");
  def is_spawn:
    (.tool_name == "Agent") or (.tool_name == "Task");
  def spawn_allowed:
    (.tool_input.subagent_type // "")
    | . == "raj" or . == "review" or . == "raj:raj" or . == "raj:review";
  def spawn_denied:
    is_spawn and (env.RAJ_SPAWN_ANY != "1") and (spawn_allowed | not);
  if is_save then
    {hookSpecificOutput:{
      hookEventName:"PreToolUse",
      permissionDecision:"deny",
      permissionDecisionReason:"blocked: `raj ctl save` is the user\u0027s own action, and an agent must never save for them. Ask the user for explicit permission and let them save; do not retry this call."
    }}
  elif spawn_denied then
    {hookSpecificOutput:{
      hookEventName:"PreToolUse",
      permissionDecision:"deny",
      permissionDecisionReason:"blocked: a Claude subagent may only be spawned as raj:raj or raj:review (or the bare names raj or review). Set RAJ_SPAWN_ANY=1 to override."
    }}
  else empty end
'
exit 0
