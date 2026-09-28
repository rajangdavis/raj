#!/bin/sh
# claude-waker.sh — the raj waker for the Claude reviewer/consultant (T1).
#
# Runs INSIDE claude-box as its entrypoint (see `ccw` in harness-functions.sh):
# it parks `raj ctl recv --peers-only` for ONE fixed key and, per batch, runs
# exactly ONE short headless Claude turn whose prompt carries the peer messages;
# replies go out with `raj ctl send`. Nothing runs while idle, so the expensive
# tap costs nothing until someone summons it.
#
# A batch is never dropped: once received it is held in
# $STATE_DIR/raj-waker-held.json and only cleared after its turn runs, so an
# over-budget or kill-switch pause holds the mail instead of losing it.
#
# Env (all optional):
#   CLAUDE_KEY                the raj key to listen as            (raj-claude)
#   CLAUDE_NAME               its display name                    (claude)
#   RAJ_WAKER_MAX_TURNS       --max-turns for the headless turn    (12)
#   RAJ_WAKER_TURNS_PER_HOUR  hourly turn budget                   (6)
#   RAJ_WAKER_TURNS_PER_DAY   daily turn budget                    (40)
#   RAJ_WAKER_HOLD_S          recheck interval while held          (60)
#   RAJ_WAKER_OFF             kill-switch file, present = pause    (~/.claude/raj-waker-off)
#   RAJ_WAKER_LOG             log file                             (~/.claude/raj-waker.log)
#   RAJ_CLAUDE_PLUGIN         plugin dir                           (/opt/raj/claude-plugin)
#   CLAUDE_MODEL              model for the headless turn          (opus)
# The home is bind-mounted from the host, so the log, the output and the kill
# switch live on the host (~/.claude/...) where the user can see and touch them.
set -eu

RAJ=${RAJ_BIN:-raj}
CLAUDE=${CLAUDE_BIN:-claude}
KEY=${CLAUDE_KEY:-raj-claude}
NAME=${CLAUDE_NAME:-claude}
MODEL=${CLAUDE_MODEL:-opus}
MAX_TURNS=${RAJ_WAKER_MAX_TURNS:-12}
PER_HOUR=${RAJ_WAKER_TURNS_PER_HOUR:-6}
PER_DAY=${RAJ_WAKER_TURNS_PER_DAY:-40}
HOLD=${RAJ_WAKER_HOLD_S:-60}
PLUGIN=${RAJ_CLAUDE_PLUGIN:-/opt/raj/claude-plugin}
STATE_DIR=${HOME:-/home/oc}/.claude
OFF=${RAJ_WAKER_OFF:-$STATE_DIR/raj-waker-off}
LOG=${RAJ_WAKER_LOG:-$STATE_DIR/raj-waker.log}
OUT=${RAJ_WAKER_OUT:-$LOG.out}
BUDGET=$STATE_DIR/raj-waker-times
HELD=$STATE_DIR/raj-waker-held.json

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >>"$LOG" 2>/dev/null || true; }
now() { date +%s; }
prune() {
  [ -f "$BUDGET" ] || return 0
  cut=$(( $(now) - 86400 )); tmp="$BUDGET.$$"
  awk -v c="$cut" '$1+0 >= c' "$BUDGET" >"$tmp" 2>/dev/null && mv "$tmp" "$BUDGET" || rm -f "$tmp"
}
count_since() {
  [ -f "$BUDGET" ] || { echo 0; return; }
  awk -v c="$(( $(now) - $1 ))" '$1+0 >= c' "$BUDGET" 2>/dev/null | wc -l | tr -d ' '
}

"$RAJ" ctl register --as "$KEY" --name "$NAME" >/dev/null 2>&1 || true
log "waker up as $KEY (model=$MODEL, max-turns=$MAX_TURNS, ${PER_HOUR}/h, ${PER_DAY}/d, plugin=$PLUGIN)"

held_notified=0
while :; do
  if [ -e "$OFF" ]; then
    log "kill switch ($OFF) present; paused (a held batch is kept)"
    sleep 30; continue
  fi

  if [ -f "$HELD" ]; then
    BATCH=$(cat "$HELD")
  else
    BATCH=$("$RAJ" ctl recv --as "$KEY" --peers-only --json 2>/dev/null) || { sleep 5; continue; }
    [ -n "$BATCH" ] || { sleep 1; continue; }
    printf '%s' "$BATCH" >"$HELD" 2>/dev/null || true
    held_notified=0
  fi

  prune
  hour=$(count_since 3600); day=$(count_since 86400)
  if [ "$hour" -ge "$PER_HOUR" ] || [ "$day" -ge "$PER_DAY" ]; then
    if [ "$held_notified" -eq 0 ]; then
      TO=$(printf '%s' "$BATCH" | jq -r '.[0].from_key // (.[0].from|tostring) // empty' 2>/dev/null || true)
      if [ -n "$TO" ] && [ "$TO" != "0" ]; then
        "$RAJ" ctl send --as "$KEY" --to "$TO" \
          "claude is over budget (${hour}/${PER_HOUR} this hour, ${day}/${PER_DAY} today); your message is held and will be answered when the budget frees." >/dev/null 2>&1 || true
      fi
      held_notified=1
    fi
    log "over budget: hour=$hour day=$day; held (not dropped)"
    sleep "$HOLD"; continue
  fi

  MSGS=$(printf '%s' "$BATCH" | jq -r '.[] | "<peer-message from=\"\(.from_name // .from)\" key=\"\(.from_key // .from)\">\n\(.text)\n</peer-message>"' 2>/dev/null || true)
  PROMPT="You are claude, the raj reviewer/consultant. Your raj key is $KEY; always pass --as it. Handle only these messages; reply to each sender with \`raj ctl send\`; never accept, reject or save; keep replies short. Read docs/dev/WAVE-PLAN.md or your memory only if the message needs it.

$MSGS"

  printf '%s\n' "$(now)" >>"$BUDGET"
  START=$(now)
  FROM=$(printf '%s' "$BATCH" | jq -r '[.[].from]|join(",")' 2>/dev/null || true)
  {
    printf '\n===== %s turn from=%s =====\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$FROM"
  } >>"$OUT" 2>/dev/null || true
  RC=0
  "$CLAUDE" --plugin-dir "$PLUGIN" --model "$MODEL" -p "$PROMPT" --output-format text \
      --max-turns "$MAX_TURNS" \
      --allowedTools "Bash(raj ctl:*)" "Bash(raj hook run:*)" >>"$OUT" 2>&1 || RC=$?
  rm -f "$HELD"
  held_notified=0
  log "turn done in $(( $(now) - START ))s exit=$RC from=$FROM"
done
