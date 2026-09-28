#!/bin/sh
# publish-single.sh — publish the saved workspace as one commit, one branch,
# one reviewable change (the `single` strategy, docs/CHANGES-ORGANIZATION.md §5a).
#
# It never touches the user's HEAD, index or working tree. The commit is built
# with a temporary index from the working tree as it is on disk (so .gitignore
# decides what is left out, exactly as `git add -A` would), parented on the
# base, and pointed at by a new branch that is not checked out. The branch is
# then pushed with plain git, and the pull-request URL is printed, built from
# the remote URL — no `gh` required for a GitHub user on regular git.
# `gh pr create` is opt-in (--pr-with gh, or RAJ_PUBLISH_PR=gh) and runs only
# when gh is on PATH. A pre-flight refuses before it pushes: a remote that is
# not a parseable github.com URL (exit 8), or secret-like content and oversized
# files in the tree being committed (exit 10).
#
# Meant to run as a raj hook the user authors (workspace tree, so it refuses
# while anything is unsaved or pending):
#
#   raj hook add publish --tree workspace -- sh examples/hooks/publish-single.sh
#
# or by hand from the repository root. POSIX sh; macOS and Linux.
#
# usage: publish-single.sh [--base REF] [--branch NAME] [--title TEXT]
#                          [--body-file FILE] [--remote NAME] [--allow-large]
#                          [--no-push] [--no-pr] [--pr-with gh] [--dry-run]
#
# Exit codes: 0 published (or planned, with --dry-run); 2 usage; 3 not ready;
# 4 not a git work tree or the base does not resolve; 5 nothing to publish;
# 6 the branch already exists; 7 push failed; 8 the remote is not a parseable
# github.com remote; 9 the requested --pr-with CLI failed; 10 the pre-flight
# found secret-like content or an oversized file in the tree being committed.
set -eu

base=""
branch=""
title=""
body_file=""
remote="origin"
push=1
pr=1
pr_with=""
dry=0
allow_large=0
[ -z "${RAJ_PUBLISH_PR:-}" ] || pr_with=$RAJ_PUBLISH_PR

die() { code=$1; shift; printf 'publish-single: %s\n' "$*" >&2; exit "$code"; }
log() { printf 'publish-single: %s\n' "$*"; }

# Normalise a remote URL to "<host> <owner>/<repo>", or fail. Understands the
# scp-like (git@github.com:owner/repo.git) and URL forms, and drops userinfo,
# a port, a leading slash and a trailing .git.
parse_remote() {
  _url=$1
  case $_url in
    *://*)
      _rest=${_url#*://}
      _host=${_rest%%/*}
      _path=${_rest#*/}
      _host=${_host#*@}
      _host=${_host%%:*}
      ;;
    *@*:*)
      _rest=${_url#*@}
      _host=${_rest%%:*}
      _path=${_rest#*:}
      ;;
    *) return 1 ;;
  esac
  _path=${_path#/}
  _path=${_path%.git}
  _path=${_path%/}
  case $_path in
    */*) ;;
    *) return 1 ;;
  esac
  _owner=${_path%%/*}
  _repo=${_path#*/}
  case $_repo in
    */*) return 1 ;;
  esac
  [ -n "$_host" ] && [ -n "$_owner" ] && [ -n "$_repo" ] || return 1
  printf '%s %s/%s\n' "$_host" "$_owner" "$_repo"
}

# Pre-flight: refuse before any commit or push. It checks the tree it is about
# to commit for secret-like content and oversized files, prints the author
# identity so a wrong one is visible before it is in a public commit, and (when
# it will push) requires a parseable github.com remote. Exit 8: the remote is
# not a parseable github.com remote. Exit 10: secret-like content, a .env*/*.pem
# file, or a file over 2 MB (unless --allow-large).
preflight() {
  _author_name=$(git config user.name 2>/dev/null || true)
  _author_email=$(git config user.email 2>/dev/null || true)
  log "author: ${_author_name:-<unset>} <${_author_email:-<unset>}>"

  _bad=0
  _hits=$(git grep -n -I \
    -e 'ghp_' -e 'github_pat_' -e 'AKIA' \
    -e 'BEGIN RSA PRIVATE KEY' -e 'BEGIN EC PRIVATE KEY' \
    -e 'BEGIN OPENSSH PRIVATE KEY' -e 'sk-' -e 'RAJ_CONTROL_TOKEN=' \
    "$tree" 2>/dev/null | sed "s/^$tree://" || true)
  if [ -n "$_hits" ]; then
    _bad=1
    printf 'publish-single: pre-flight: secret-like content in the tree being committed:\n%s\n' "$_hits" >&2
  fi
  for _p in $(git ls-tree -r --name-only "$tree" 2>/dev/null); do
    case ${_p##*/} in
      .env*|*.pem)
        _bad=1
        printf 'publish-single: pre-flight: secret-bearing file in the tree being committed: %s\n' "$_p" >&2
        ;;
    esac
  done
  if [ "$_bad" = 1 ]; then
    die 10 "refusing to publish the tree above; remove the hits, or publish elsewhere"
  fi

  if [ "$allow_large" != 1 ]; then
    _large=$(git ls-tree -r -l "$tree" 2>/dev/null | while IFS='	' read -r _meta _p; do
      _sz=${_meta##* }
      case $_sz in ''|*[!0-9]*) continue ;; esac
      [ "$_sz" -le 2097152 ] || printf '%s (%s bytes)\n' "$_p" "$_sz"
    done)
    if [ -n "$_large" ]; then
      printf 'publish-single: pre-flight: files over 2 MB in the tree being committed:\n%s\n' "$_large" >&2
      die 10 "pass --allow-large to publish oversized files"
    fi
  fi

  if [ "$push" = 1 ]; then
    if [ -z "$remote_url" ]; then
      die 8 "pre-flight: no remote URL for '$remote' (saw: none); cannot confirm a github.com push target"
    fi
    if [ "$remote_host" != github.com ]; then
      die 8 "pre-flight: remote '$remote' is not a github.com remote (saw: $remote_url)"
    fi
  fi
}

while [ $# -gt 0 ]; do
  case $1 in
    --base) base=${2:?--base needs a ref}; shift 2 ;;
    --branch) branch=${2:?--branch needs a name}; shift 2 ;;
    --title) title=${2:?--title needs text}; shift 2 ;;
    --body-file) body_file=${2:?--body-file needs a file}; shift 2 ;;
    --remote) remote=${2:?--remote needs a name}; shift 2 ;;
    --no-push) push=0; pr=0; shift ;;
    --no-pr) pr=0; shift ;;
    --pr-with) pr_with=${2:?--pr-with needs a value}; shift 2 ;;
    --allow-large) allow_large=1; shift ;;
    --dry-run) dry=1; shift ;;
    -h|--help) sed -n '2,31p' "$0"; exit 0 ;;
    *) die 2 "unknown argument: $1" ;;
  esac
done

case $pr_with in
  ""|gh) ;;
  *) die 2 "unsupported --pr-with/RAJ_PUBLISH_PR: $pr_with" ;;
esac

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die 4 "not inside a git work tree"
root=$(git rev-parse --show-toplevel)
cd "$root"

# Ready means nothing unsaved or pending in the editor, so the tree on disk is
# the tree the user reviewed. RAJ_PUBLISH_SKIP_STATUS=1 skips it for a repo not
# open in raj (and for the test).
if [ "${RAJ_PUBLISH_SKIP_STATUS:-0}" != 1 ] && command -v raj >/dev/null 2>&1; then
  if ! status_out=$(raj ctl status 2>&1); then
    # Not ready. The status line names why: unsaved work, a pending set, or
    # rejected text left in the view that only `clear` disposes. Print it
    # rather than refusing anonymously, so the next step is in the message.
    printf '%s\n' "$status_out" >&2
    die 3 "the workspace is not ready (raj ctl status): save accepted work, settle pending sets, and clear rejected sets to dispose of them"
  fi
fi

# The base: an explicit ref, else the remote's main, else local main.
if [ -z "$base" ]; then
  if git rev-parse --verify --quiet "refs/remotes/$remote/main" >/dev/null; then
    base="$remote/main"
  else
    base="main"
  fi
fi
base_sha=$(git rev-parse --verify --quiet "$base^{commit}") || die 4 "base $base does not resolve to a commit"
# The PR targets the branch name, without the remote prefix.
base_branch=${base#"$remote"/}

# Build the tree from the working tree with a private index: read the base, then
# stage everything on disk that .gitignore allows, deletions included. The
# user's own index is never read or written.
tmp_index=$(mktemp "${TMPDIR:-/tmp}/publish-single-index.XXXXXX")
trap 'rm -f "$tmp_index" "${msg_file:-}"' EXIT
GIT_INDEX_FILE=$tmp_index git read-tree "$base_sha"
GIT_INDEX_FILE=$tmp_index git add -A .
tree=$(GIT_INDEX_FILE=$tmp_index git write-tree)
base_tree=$(git rev-parse "$base_sha^{tree}")
[ "$tree" != "$base_tree" ] || die 5 "nothing to publish: the working tree matches $base"

[ -n "$branch" ] || branch="raj/publish/$(date +%Y%m%d-%H%M%S)"
[ -n "$title" ] || title="raj: publish $(date +%Y-%m-%d)"

# The remote URL decides the pull-request URL. A remote we cannot parse is not
# fatal here: the push still happens and the URL step reports honestly.
remote_url=$(git remote get-url "$remote" 2>/dev/null) || remote_url=""
remote_host=""
remote_slug=""
if [ -n "$remote_url" ]; then
  if _info=$(parse_remote "$remote_url"); then
    remote_host=${_info%% *}
    remote_slug=${_info#* }
  fi
fi

if [ "$dry" = 1 ]; then
  log "would publish onto $base ($base_sha) as branch $branch"
  log "title: $title"
  git diff --stat "$base_sha" "$tree" | tail -n 25
  [ "$push" = 1 ] && log "would push $branch to $remote and set its upstream"
  if [ "$pr" = 1 ]; then
    if [ "$pr_with" = gh ]; then
      log "would open a PR $branch -> $base_branch with gh"
    elif [ "$remote_host" = github.com ]; then
      log "would print the pull-request URL for $branch -> $base_branch"
    else
      log "would push and report the remote (no pull-request URL for this host)"
    fi
  fi
  exit 0
fi

# The pre-flight sees the content the commit is about to hold, so a secret or
# an oversized file is refused here, before the commit or push exists.
preflight

if git rev-parse --verify --quiet "refs/heads/$branch" >/dev/null; then
  die 6 "branch $branch already exists; pass --branch with a new name"
fi

msg_file=$(mktemp "${TMPDIR:-/tmp}/publish-single-msg.XXXXXX")
{
  printf '%s\n\n' "$title"
  if [ -n "$body_file" ]; then cat "$body_file"; else git diff --stat "$base_sha" "$tree"; fi
} > "$msg_file"
commit=$(git commit-tree "$tree" -p "$base_sha" -F "$msg_file")
# Create only: the empty old-value refuses to move an existing ref.
git update-ref "refs/heads/$branch" "$commit" ""
log "committed $commit on new branch $branch (HEAD and index untouched)"

if [ "$push" = 1 ]; then
  # GitLab can create the merge request from the push itself; every other host
  # gets the plain push, and the URL is reported after it.
  push_opts=""
  if [ "$pr" = 1 ] && [ "$pr_with" != gh ] && [ "$remote_host" = gitlab.com ]; then
    push_opts="-o merge_request.create -o merge_request.target=$base_branch"
  fi
  # shellcheck disable=SC2086
  git push --set-upstream $push_opts "$remote" "$branch" || die 7 "push of $branch to $remote failed"
  log "pushed $branch to $remote (upstream set)"
fi

if [ "$pr" = 1 ]; then
  if [ "$pr_with" = gh ]; then
    command -v gh >/dev/null 2>&1 || die 9 "--pr-with gh was requested but gh is not on PATH"
    if [ -n "$body_file" ]; then
      url=$(gh pr create --base "$base_branch" --head "$branch" --title "$title" --body-file "$body_file") || die 9 "gh pr create failed"
    else
      url=$(gh pr create --base "$base_branch" --head "$branch" --title "$title" --body-file "$msg_file") || die 9 "gh pr create failed"
    fi
    printf 'publish-single: open a pull request: %s\n' "$url"
  else
    case $remote_host in
      github.com)
        printf 'publish-single: open a pull request: https://github.com/%s/pull/new/%s\n' "$remote_slug" "$branch"
        ;;
      gitlab.com)
        log "GitLab creates the merge request from the push options; its URL is not constructed here"
        printf 'publish-single: remote: %s\n' "$remote_url"
        ;;
      *)
        die 8 "could not construct the pull-request URL for this remote: ${remote_url:-$remote}; the branch is pushed"
        ;;
    esac
  fi
fi
