#!/bin/sh
# claude-waker.test.sh — stub test of the waker's kill switch, budget/hold and
# turn firing. No docker and no claude: `raj` and `claude` are PATH stubs.
# POSIX sh, no `timeout` (macOS). Exit non-zero on any mismatch.
set -eu
here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
waker=$here/claude-waker.sh
fails=0
fail() { printf 'claude-waker.test: FAIL: %s\n' "$*" >&2; fails=$((fails+1)); }

d=$(mktemp -d)
mkdir -p "$d/bin" "$d/.claude"
cat > "$d/bin/raj" <<'R'
#!/bin/sh
case "$*" in
  *"ctl register"*) exit 0 ;;
  *"ctl who"*)      echo '[]'; exit 0 ;;
  *"ctl send"*)     echo "$*" >>"$HOME/sent"; exit 0 ;;
  *"ctl recv"*)     if [ ! -e "$HOME/got" ]; then touch "$HOME/got"; echo '[{"from":2,"from_key":"raj-x","from_name":"deepseek","text":"review please"}]'; exit 0; fi; sleep 2; exit 0 ;;
esac
exit 0
R
cat > "$d/bin/claude" <<'C'
#!/bin/sh
echo called >>"$HOME/claude-called"
echo "$*" >>"$HOME/claude-args"
exit 0
C
chmod +x "$d/bin/raj" "$d/bin/claude"

start_waker() {
  HOME="$d" RAJ_BIN="$d/bin/raj" CLAUDE_BIN="$d/bin/claude" \
  RAJ_WAKER_OFF="$d/.claude/off" RAJ_WAKER_LOG="$d/.claude/log" \
  RAJ_WAKER_HOLD_S=1 \
  RAJ_WAKER_TURNS_PER_HOUR="${PER_HOUR:-6}" RAJ_WAKER_TURNS_PER_DAY="${PER_DAY:-40}" \
  sh "$waker" & WPID=$!
}
stop_waker() { kill "$WPID" 2>/dev/null || true; wait "$WPID" 2>/dev/null || true; }
reset() { rm -f "$d/.claude/off" "$d/claude-called" "$d/claude-args" "$d/got" "$d/sent" "$d/.claude/raj-waker-held.json"; : > "$d/.claude/raj-waker-times"; }

# 1) kill switch present -> no turn
reset; touch "$d/.claude/off"
PER_HOUR=6 start_waker; sleep 2; stop_waker
[ -e "$d/claude-called" ] && fail "kill switch present but claude was called"

# 2) over budget HOLDS the batch; a freed budget then runs it with the held text
reset
i=0; while [ $i -lt 10 ]; do date +%s >>"$d/.claude/raj-waker-times"; i=$((i+1)); done
PER_HOUR=1 start_waker
sleep 2
[ -e "$d/claude-called" ] && fail "over budget but claude was called"
[ -f "$d/.claude/raj-waker-held.json" ] || fail "over budget did not hold the batch"
grep -q "over budget" "$d/sent" 2>/dev/null || fail "over budget did not reply once"
: > "$d/.claude/raj-waker-times"
sleep 3
stop_waker
[ -e "$d/claude-called" ] || fail "held batch never ran after the budget freed"
grep -q "review please" "$d/claude-args" 2>/dev/null || fail "the held message was not in the prompt"

# 3) in budget -> one turn fires
reset
PER_HOUR=6 start_waker; sleep 3; stop_waker
[ -e "$d/claude-called" ] || fail "in budget but claude was never called"

if [ "$fails" -ne 0 ]; then printf 'claude-waker.test: %d case(s) failed\n' "$fails" >&2; exit 1; fi
printf 'claude-waker.test: ok\n'
