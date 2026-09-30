#!/bin/sh
# raj-usage.sh - one line: how much Claude session is left.
#   raj-usage.sh --save   statusLine mode: read Claude Code's JSON on stdin,
#                         keep its rate_limits, print the line
#   raj-usage.sh          print the line from the last saved copy
# Needs jq (built into macOS 15+; otherwise: brew install jq).
CACHE="$HOME/.claude/raj-usage.json"
NONE="session: not available from this build - use /usage"

when() { date -r "$1" '+%a %H:%M' 2>/dev/null || date -d "@$1" '+%a %H:%M'; }

if [ "$1" = "--save" ]; then
  jq -c '.rate_limits // empty' > "$CACHE.tmp" 2>/dev/null
  if [ -s "$CACHE.tmp" ]; then mv "$CACHE.tmp" "$CACHE"; else rm -f "$CACHE.tmp"; fi
fi

[ -s "$CACHE" ] || { echo "$NONE"; exit 0; }
now=$(date +%s)
out=""
for w in five_hour:5h seven_day:7d; do
  pct=$(jq -r ".${w%%:*}.used_percentage // empty" "$CACHE")
  at=$(jq -r ".${w%%:*}.resets_at // empty" "$CACHE")
  [ -n "$pct" ] && [ -n "$at" ] || continue
  if [ "$now" -ge "$at" ]; then
    part="${w#*:}: 100% left (reset $(when "$at"))"
  else
    part="${w#*:}: $(awk -v p="$pct" 'BEGIN{printf "%d", 100-p+0.5}')% left, resets $(when "$at")"
  fi
  out="$out${out:+ | }$part"
done
echo "${out:-$NONE}"
