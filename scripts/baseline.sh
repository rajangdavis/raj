#!/bin/sh
# baseline.sh — anchor the saved workspace as a linear stack of LOCAL commits,
# one commit per merge request (one per top-level area). Local only: no remote,
# no push, no PR, no forge.
#
# The user's HEAD, index and working tree are never touched: the tree is built
# with a temporary GIT_INDEX_FILE and the commits with commit-tree, and the
# branch ref is created only when it is absent. This is Track S step S1 (the
# base the change graph diffs, exports and publishes against); refs still move
# only at a human publish step.
#
# Run from the repository root, or as a raj hook the user authors:
#   raj hook add baseline --tree workspace -- sh scripts/baseline.sh
#
# usage: baseline.sh [--base REF] [--branch NAME] [--dry-run]
# Exit: 0 done (or planned with --dry-run); 2 usage; 3 not ready; 4 not a git
# work tree or the base does not resolve; 5 nothing to baseline; 6 the branch
# already exists.
set -eu

base=""
branch=""
dry=0

die() { code=$1; shift; printf 'baseline: %s\n' "$*" >&2; exit "$code"; }
log() { printf 'baseline: %s\n' "$*"; }

while [ $# -gt 0 ]; do
  case $1 in
    --base) base=${2:?--base needs a ref}; shift 2 ;;
    --branch) branch=${2:?--branch needs a name}; shift 2 ;;
    --dry-run) dry=1; shift ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) die 2 "unknown argument: $1" ;;
  esac
done

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die 4 "not inside a git work tree"
root=$(git rev-parse --show-toplevel)
cd "$root"

# Ready means nothing unsaved or pending in the editor, so the tree on disk is
# the tree the user reviewed. RAJ_BASELINE_SKIP_STATUS=1 skips it for a repo not
# open in raj.
if [ "${RAJ_BASELINE_SKIP_STATUS:-0}" != 1 ] && command -v raj >/dev/null 2>&1; then
  raj ctl status >/dev/null 2>&1 || die 3 "the workspace is not ready (raj ctl status): save or settle pending changes first"
fi

if [ -z "$base" ]; then
  if git rev-parse --verify --quiet refs/heads/main >/dev/null; then base=main; else base=HEAD; fi
fi
base_sha=$(git rev-parse --verify --quiet "$base^{commit}") || die 4 "base $base does not resolve to a commit"

tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/raj-baseline.XXXXXX")
trap 'rm -rf "$tmpdir"' EXIT
idx=$tmpdir/index
paths=$tmpdir/paths
changed=$tmpdir/changed

# Every path that differs from the base: tracked changes and untracked files,
# with .gitignore deciding what is left out (as `git add -A` would).
{ git diff --name-only "$base_sha"; git ls-files --others --exclude-standard; } | sort -u > "$changed"

# area = the first path component, or "root" for a top-level file.
: > "$paths"
while IFS= read -r p; do
  [ -n "$p" ] || continue
  case "$p" in
    */*) a=${p%%/*} ;;
    *) a=root ;;
  esac
  printf '%s\t%s\n' "$a" "$p" >> "$paths"
done < "$changed"

areas=$(cut -f1 "$paths" | sort -u)
[ -n "$areas" ] || die 5 "nothing to baseline: the working tree matches $base"

[ -n "$branch" ] || branch="raj/baseline"

if [ "$dry" = 1 ]; then
  log "base $base ($base_sha); would build one commit per area on branch $branch"
  for a in $areas; do
    n=$(awk -F'\t' -v a="$a" '$1==a' "$paths" | wc -l | tr -d ' ')
    log "  $a: $n path(s)"
  done
  exit 0
fi

git rev-parse --verify --quiet "refs/heads/$branch" >/dev/null && die 6 "branch $branch already exists; pass --branch with a new name"

GIT_INDEX_FILE=$idx git read-tree "$base_sha"
prev=$base_sha
for a in $areas; do
  n=$(awk -F'\t' -v a="$a" '$1==a' "$paths" | wc -l | tr -d ' ')
  if [ "$a" = root ]; then
    # A top-level file per path; a space in a filename would split here, which
    # none of this repo's root files has.
    set -- $(awk -F'\t' -v a="$a" '$1==a{print $2}' "$paths")
  else
    set -- "$a"
  fi
  GIT_INDEX_FILE=$idx git add -A -- "$@"
  tree=$(GIT_INDEX_FILE=$idx git write-tree)
  commit=$(git commit-tree "$tree" -p "$prev" -m "baseline($a): the saved backlog ($n paths)")
  log "  $a -> $commit"
  prev=$commit
done

git update-ref "refs/heads/$branch" "$prev" ""
log "baseline $branch = $prev over $base; HEAD, index and worktree untouched"
