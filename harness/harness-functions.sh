# shellcheck shell=bash
# raj harness containers: build + run, one per agent runtime.
#
# Source this from your shell:  source ~/Desktop/projects/raj/harness/harness-functions.sh
#
# Every Dockerfile lives in the repo under harness/, and the docker build
# context is the REPO ITSELF, so the Dockerfile COPY paths are repo-relative
# (bin/, skills/, harness/...). Override RAJ_REPO if the repo moves.
#
# Fixes over the original oc(): an assertion that RAJ_CONTROL_TOKEN is exported
# (it was silent when missing), and agent args placed correctly. No --add-host:
# on Docker Desktop host.docker.internal already reaches host loopback, and
# forcing the host-gateway IP repoints it at the Linux bridge, which cannot
# reach a daemon bound to 127.0.0.1.

RAJ_REPO="${RAJ_REPO:-$HOME/Desktop/projects/raj}"

# Build the linux raj binary every image bakes.
_raj_linux() {
  ( cd "$RAJ_REPO" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/raj-linux ./cmd/raj )
}

# Build one harness image from harness/<Dockerfile>, with the repo as context.
_bldbox() {
  local dockerfile="$1" image="$2"
  _raj_linux || return 1
  docker build -t "$image" -f "$RAJ_REPO/harness/$dockerfile" "$RAJ_REPO"
}

# Rebuild raj for the host and all three harness images.
bldraj()  { _raj_linux || return 1; ( cd "$RAJ_REPO" && make build ); }
bldoc()   { _bldbox Dockerfile.opencode opencode-box; }

# Build the opencode v2 image (harness/Dockerfile.opencode2).
bldoc2()  { _bldbox Dockerfile.opencode2 opencode2-box; }
bldclaude() { _bldbox Dockerfile.claude claude-box; }
bldcodex()  { _bldbox Dockerfile.codex  codex-box;  }
bldall()  { bldraj && bldoc && bldclaude && bldcodex; }

# The `cycle` hook (WAVE-PLAN Track L, L3; HOOKS-SPEC §10) turns a landed wave
# into a running build: check, rebuild raj for the host and every harness image,
# refresh the CLI in the running containers, announce, restart the editor and
# wait for the agents to re-register. The user authors the row once on the host
# (agent, workspace tree, detached, may_write); rcycle runs it from the repo:
#
#   raj hook add cycle --agent --tree workspace --detach --may-write \
#     --timeout-ms 1800000 -- sh scripts/raj-cycle.sh
#   rcycle
rcycle() { ( cd "$RAJ_REPO" && raj hook run cycle ); }

# Common docker args every harness needs. The image name is first; the port is
# second; the rest go to the container's entrypoint. _oc_run is the interactive
# form; _oc_docker takes extra `docker run` flags first, ended by `--`, which is
# how a detached worker gets its name.
_oc_run() { _oc_docker -it -- "$@"; }

_oc_docker() {
  local dargs=()
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do dargs+=("$1"); shift; done
  [ $# -gt 0 ] && shift
  local image="$1"; shift
  local port="${1:-7391}"; shift || true
  if [ -z "${RAJ_CONTROL_TOKEN:-}" ]; then
    echo "harness: RAJ_CONTROL_TOKEN is not set or not exported; export it first" >&2
    return 1
  fi
  # Host homes for the agents' own state (login, sessions, history). Created
  # here so docker does not make a missing bind source for us.
  local claude_home="${RAJ_CLAUDE_HOME:-$HOME/.claude}"
  mkdir -p "$claude_home"
  # Codex is supported, not required: its host home is mounted only when one
  # exists (or RAJ_CODEX_HOME names one); otherwise the codex-data volume
  # keeps its state, and nothing is created on the host for it.
  local codex_home="codex-data"
  if [ -n "${RAJ_CODEX_HOME:-}" ] || [ -d "$HOME/.codex" ]; then
    codex_home="${RAJ_CODEX_HOME:-$HOME/.codex}"
  fi
  docker run --rm "${dargs[@]}" \
    -e RAJ_CONTROL_ADDR="tcp://host.docker.internal:${port}" \
    -e RAJ_CONTROL_TOKEN="$RAJ_CONTROL_TOKEN" \
    -e OPENCODE_API_KEY="${OPENCODE_API_KEY:-}" \
    -e DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY:-}" \
    -e ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-}" \
    -e OPENAI_API_KEY="${OPENAI_API_KEY:-}" \
    -v oc-data:/home/oc/.local/share/opencode \
    -v "$claude_home:/home/oc/.claude" \
    -e CLAUDE_CONFIG_DIR=/home/oc/.claude \
    -v "$codex_home:/home/oc/.codex" \
    "$image" "$@"
}

# opencode: `--agent raj` is the first container arg, before any the caller adds.
# `oc` is the primary session and continues the last one; the image no longer
# bakes --continue, so a worker (below) can start its own session instead of
# colliding with this one.
oc() { local p="${1:-7391}"; shift 2>/dev/null || true; _oc_run opencode-box "$p" --agent "${RAJ_OC_AGENT:-raj}" --continue "$@"; }

# The same primary session, running the `orchestrated` agent: it takes the
# Claude orchestrator's WAVE-PLAN briefs without asking you first, fans them
# out to raj/review subagents, and still asks when a brief strays from its item
# (harness/opencode/agents/orchestrated.md). Inside a running session, Tab
# switches between the raj and orchestrated agents without a restart.
oco() { RAJ_OC_AGENT=orchestrated oc "$@"; }

# opencode v2: the same wiring as oc(), against the opencode2-box image and
# its own oc2-data volume (v2 has its own database and never shares v1's).
# v2's flags are confirmed from `opencode --help` (2026-09-27): --auto,
# --continue, --session and --prompt exist; there is NO --agent, so the primary
# agent comes from default_agent in opencode2/opencode.jsonc.
_oc2_run() { _oc2_docker -it -- "$@"; }

# Deliberately a copy of _oc_docker rather than a parameterised shared helper:
# the v1 helpers stay untouched, and only the data volume differs.
_oc2_docker() {
  local dargs=()
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do dargs+=("$1"); shift; done
  [ $# -gt 0 ] && shift
  local image="$1"; shift
  local port="${1:-7391}"; shift || true
  if [ -z "${RAJ_CONTROL_TOKEN:-}" ]; then
    echo "harness: RAJ_CONTROL_TOKEN is not set or not exported; export it first" >&2
    return 1
  fi
  local claude_home="${RAJ_CLAUDE_HOME:-$HOME/.claude}"
  mkdir -p "$claude_home"
  local codex_home="codex-data"
  if [ -n "${RAJ_CODEX_HOME:-}" ] || [ -d "$HOME/.codex" ]; then
    codex_home="${RAJ_CODEX_HOME:-$HOME/.codex}"
  fi
  docker run --rm "${dargs[@]}" \
    -e RAJ_CONTROL_ADDR="tcp://host.docker.internal:${port}" \
    -e RAJ_CONTROL_TOKEN="$RAJ_CONTROL_TOKEN" \
    -e OPENCODE_API_KEY="${OPENCODE_API_KEY:-}" \
    -e DEEPSEEK_API_KEY="${DEEPSEEK_API_KEY:-}" \
    -e ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-}" \
    -e OPENAI_API_KEY="${OPENAI_API_KEY:-}" \
    -v oc2-data:/home/oc/.local/share/opencode \
    -v "$claude_home:/home/oc/.claude" \
    -e CLAUDE_CONFIG_DIR=/home/oc/.claude \
    -v "$codex_home:/home/oc/.codex" \
    "$image" "$@"
}

# opencode v2 primary session: the config's default_agent (raj) is the session
# agent; --auto and --continue are confirmed v2 flags. v2 has no --agent, so
# there is no oco2 switch: a different primary agent would need a second config.
oc2() { local p="${1:-7391}"; shift 2>/dev/null || true; _oc2_run opencode2-box "$p" --auto --continue "$@"; }
oco2() {
  echo "harness: opencode v2 takes its primary agent from config (default_agent), not the CLI; oco2 (orchestrated) is unavailable (OPENCODE-V2.md GAP)" >&2
  return 1
}

# DeepSeek workers: extra opencode sessions, detached, one container each, so
# the orchestrator can brief several implementers in parallel over `raj ctl
# send`. Each runs the `worker` agent (harness/opencode/agents/worker.md),
# registers as deepseek-<name>, and raj-mail wakes it on mail. Starting a
# worker is your standing approval: it acts on an orchestrator brief for a
# WAVE-PLAN item without a second confirmation, and still asks when a brief
# strays from its item or claim set.
#
#   ocw w1            start worker w1 (detached)
#   ocw w1 --task T   start worker w1 with its writes filed under task T
#   ocw-attach w1     watch or talk to it (detach: ctrl-p ctrl-q)
#   ocw-ls            list running workers
#   ocw-stop w1       stop it (its session stays in opencode's db)
ocw() {
  local name="${1:?usage: ocw NAME [PORT] [--task TASK]}"; shift 2>/dev/null || true
  local p="${1:-7391}"; shift 2>/dev/null || true
  # The task rides RAJ_TASK into the container, where `raj ctl` binds it to
  # every hello the worker makes; a register then pins it on its participant so
  # every write is filed under the brief's work. RAJ_TASK in the caller's
  # environment is the default, and an explicit --task overrides it.
  local task="${RAJ_TASK:-}"
  if [ "${1:-}" = "--task" ]; then task="${2:?ocw --task needs a value}"; shift 2 || true; fi
  # The rules live in the `worker` agent (harness/opencode/agents/worker.md);
  # the prompt only names the worker and starts it.
  local prompt="You are raj worker ${name}. Register now as deepseek-${name}, then stop and wait for briefs."
  _oc_docker -dit --name "raj-oc-${name}" -e "RAJ_TASK=${task}" -- opencode-box "$p" --agent worker --prompt "$prompt" \
    && echo "worker ${name} started; attach with: ocw-attach ${name}"
}

ocw-attach() { docker attach "raj-oc-${1:?usage: ocw-attach NAME}"; }
ocw-ls()     { docker ps --filter name=raj-oc- --format '{{.Names}}\t{{.Status}}'; }
ocw-stop()   { docker stop "raj-oc-${1:?usage: ocw-stop NAME}"; }

# claude code: the Dockerfile entrypoint already passes --plugin-dir. ~/.claude
# on the host (RAJ_CLAUDE_HOME overrides) is bind-mounted and CLAUDE_CONFIG_DIR
# points there, so sessions and the login (.claude.json) survive the --rm
# container. Extra args reach claude: `cc 7391 --resume <id>`, `cc 7391 -c`.
cc() { local p="${1:-7391}"; shift 2>/dev/null || true; _oc_run claude-box "$p" "$@"; }

# Claude waker (T1b): a DETACHED claude-box whose entrypoint is the waker loop,
# so claude is reachable on demand and idle costs nothing. CLAUDE_KEY defaults
# to raj-claude; refuse to start if that key is already connected (never two
# consumers on one key). The waker's log and kill switch live in the host home
# (~/.claude) via the mounted home. Stop with ccw-stop; pause with
# `touch ~/.claude/raj-waker-off`.
ccw() {
  local p="${1:-7391}"
  local key="${CLAUDE_KEY:-raj-claude}"
  if raj ctl who --live --json 2>/dev/null | jq -e --arg k "$key" '[.[]|select((.identity//.Identity)==$k)]|length>0' >/dev/null 2>&1; then
    echo "harness: $key is already connected; refusing a second consumer" >&2
    return 1
  fi
  # Forward the waker's tuning env into the container when the caller set it,
  # so `RAJ_WAKER_TURNS_PER_HOUR=12 ccw` actually reaches the loop.
  local wenv=()
  local v
  for v in RAJ_WAKER_TURNS_PER_HOUR RAJ_WAKER_TURNS_PER_DAY RAJ_WAKER_MAX_TURNS RAJ_WAKER_HOLD_S RAJ_WAKER_OFF RAJ_WAKER_LOG; do
    [ -n "${!v:-}" ] && wenv+=(-e "$v=${!v}")
  done
  _oc_docker -dit --name raj-cc-waker --entrypoint /opt/raj/claude-plugin/scripts/claude-waker.sh \
    -e CLAUDE_KEY="$key" ${wenv[@]+"${wenv[@]}"} -- claude-box "$p" \
    && echo "waker started (key $key); log: ${RAJ_CLAUDE_HOME:-$HOME/.claude}/raj-waker.log; stop: ccw-stop"
}
ccw-stop() { docker stop raj-cc-waker; }
ccw-log()  { tail -n "${1:-40}" "${RAJ_CLAUDE_HOME:-$HOME/.claude}/raj-waker.log"; }

# codex: the entrypoint seeds ~/.codex from the image, then runs `codex
# --ask-for-approval never`. Its state persists in the codex-data volume, or in
# ~/.codex on the host once that exists (RAJ_CODEX_HOME overrides). Extra args
# reach codex.
# First run: open `/hooks` and trust raj-guard, or deploy managed hooks.
cx() { local p="${1:-7391}"; shift 2>/dev/null || true; _oc_run codex-box "$p" "$@"; }
