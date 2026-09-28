#!/bin/sh
# raj-codex-entrypoint — seed CODEX_HOME from the image, then start Codex.
#
# `cx` bind-mounts the host's ~/.codex over CODEX_HOME so the login, sessions
# and history survive the --rm container. That mount hides anything the image
# put there, so the image keeps its copies under /opt/raj/codex and this script
# puts them in place on every start:
#
# - the guard (hooks.json, hooks/raj-guard.sh) is policy the image owns, so it
#   is refreshed every time and a rebuild always ships the current one;
# - config.toml is seeded only when missing, so the user's own edits (model,
#   hook trust, anything Codex writes back) are never overwritten.
set -eu

home="${CODEX_HOME:-$HOME/.codex}"
mkdir -p "$home/hooks"
cp /opt/raj/codex/hooks.json "$home/hooks.json"
cp /opt/raj/codex/hooks/raj-guard.sh "$home/hooks/raj-guard.sh"
chmod 0755 "$home/hooks/raj-guard.sh"
[ -e "$home/config.toml" ] || cp /opt/raj/codex/config.toml "$home/config.toml"

exec codex --ask-for-approval never "$@"
