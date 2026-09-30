#!/bin/sh
# no-ignored-source.sh — fail when .gitignore hides a source-like file.
#
# The bug this guards: .gitignore's first pattern was a bare `raj`, meant for
# the binary built at the repository root. It also matched the DIRECTORY
# component of `cmd/raj/`, so `cmd/raj/main_test.go` — a real, untracked test
# file — was silently ignored: `git add -A` would never have committed it, CI
# would never have run it, and nothing would have said so. Any unanchored
# pattern can quietly swallow source the same way.
#
# The check asks git for every ignored untracked path and refuses any that
# looks like source and is not on the explicit allowlist below. Run from the
# repository root, or as a raj hook the user authors (workspace tree, because
# it reads the saved .gitignore):
#
#   raj hook add ignored-source --agent --tree workspace -- \
#       sh examples/hooks/no-ignored-source.sh
#
# Source-like suffixes: .go .md .sh .ts .tsx .json .yml .yaml .mod .sum.
#
# Exit: 0 clean or no git work tree (skipped), 1 an ignored source-like path
# is not allowed.
set -eu

# No repository (a projected/scratch tree, say) means there is no .gitignore to
# ask about: skip rather than fail, so `make check` passes there. CI's real
# checkout and the registered workspace-tree hook still run the check.
root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  printf 'no-ignored-source: not a git work tree, skipped\n'
  exit 0
}
cd "$root"

# allowed — the explicit allowlist. Every entry is an exact prefix, and every# entry carries the reason it is safe to ignore. Extend this only with a
# reason. Matching is against the path git prints (root-relative, no leading
# slash) and against its /-anchored form, so a root-anchored pattern such as
# /raj still matches what git reports as `raj`.
is_allowed() {
  case $1 in
    # bin/ — build output: the binary and the Linux cross-build land here.
    bin/*) ;;
    # .raj/ — raj's own per-workspace state (session, hook store); not source.
    .raj/*) ;;
    # docs/dev/ — the dev-process docs are deliberately out of the product.
    docs/dev/*) ;;
    # scripts/raj-cycle.sh — the dev cycle driver is out of the product.
    scripts/raj-cycle.sh*) ;;
    # scripts/raj-cycle.test.sh — the cycle test, out of the product too.
    scripts/raj-cycle.test.sh*) ;;
    # scripts/call-runs.mjs — the dev census helper, out of the product too.
    scripts/call-runs.mjs*) ;;
    # .tool-versions — the local toolchain pin, not source.
    .tool-versions*) ;;
    # /raj — the repo-root binary the first .gitignore pattern is for.
    /raj|/raj/*) ;;
    *) return 1 ;;
  esac
  return 0
}
allowed() { is_allowed "$1" || is_allowed "/$1"; }

tmp=$(mktemp "${TMPDIR:-/tmp}/no-ignored-source.XXXXXX")
trap 'rm -f "$tmp"' EXIT
git ls-files --others --ignored --exclude-standard --full-name > "$tmp"

n=0
while IFS= read -r p; do
  [ -n "$p" ] || continue
  case $p in
    *.go|*.md|*.sh|*.ts|*.tsx|*.json|*.yml|*.yaml|*.mod|*.sum) ;;
    *) continue ;;
  esac
  allowed "$p" && continue
  printf 'no-ignored-source: ignored source-like path: %s\n' "$p"
  n=$((n + 1))
done < "$tmp"

if [ "$n" -gt 0 ]; then
  printf '%s\n' "no-ignored-source: ANCHOR the .gitignore pattern with a leading slash so it stops matching a directory component, or ADD the path to the allowlist with a reason if it is deliberately out of the product"
  exit 1
fi
