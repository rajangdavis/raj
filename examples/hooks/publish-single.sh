#!/bin/sh
# publish-single.sh — push the wave's exported artifact as one branch, one
# reviewable change (the `single` strategy, docs/CHANGES-ORGANIZATION.md §5a).
#
# The artifact is the commit the S3 export already wrote (intent/land.go): the
# reviewed projection materialised as one commit parented on the base. It is
# immutable, so this script never builds a commit and never reads the working
# tree. It pushes exactly the commit named by --commit and opens the change.
#
# Building a commit here from the saved working tree was the defect this
# replaces: the disk could move after the export, so a push "off main" could
# carry changes the reviewer never saw, and a changed title or body would
# change the sha. The artifact's tree is the only content this script inspects.
#
# It never touches the user's HEAD, index or working tree, and it never
# force-pushes. A second publish of an unchanged artifact is a no-op that
# reprints the pull-request URL; a second publish whose artifact moved on is a
# fast-forward push, and anything else is refused rather than forced.
#
# Meant to run as a raj hook the user authors (workspace tree):
#
#   raj hook add publish --tree workspace -- sh examples/hooks/publish-single.sh
#
# or by hand from the repository root. POSIX sh; macOS and Linux.
#
# usage: publish-single.sh --commit SHA [--base REF] [--branch NAME]
#                          [--remote NAME] [--allow-large] [--no-push]
#                          [--no-pr] [--pr-with gh] [--dry-run]
#
# Exit codes: 0 published (or planned, with --dry-run); 2 usage (a missing
# --commit included); 4 not a git work tree, --commit does not resolve to a
# commit, or the base does not resolve; 5 nothing to publish (the artifact's
# the base); 6 a local branch of the same name diverged (not a fast-forward);
# 7 push failed; 8 the remote is not a parseable github.com remote; 9 the
# requested --pr-with CLI failed; 10 the pre-flight found secret-like content
# or an oversized file in the artifact's tree; 11 the branch is unsafe (it is
# the base, a protected trunk name, or the branch checked out now); 12 the
# remote branch is not a fast-forward of the commit being published; 13 the
# artifact is not parented on the base. (3 is retired: the artifact is
# immutable, so the editor's unsaved state is no longer a gate.)
set -eu

commit=""
base=""
branch=""
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

# large_files prints one "path (N bytes)" line per file in tree $1 that is
# over 2 MB. It is a function rather than an inline command substitution
# because bash before 4.0 scans a substitution for its closing paren instead
# of parsing it recursively: the `)` that ends a case pattern inside one is
# read as the end of the substitution, and the file fails to parse on the
# host's bash 3.2.
large_files() {
  git ls-tree -r -l "$1" 2>/dev/null | while IFS='	' read -r _meta _p; do
    _sz=${_meta##* }
    case $_sz in ''|*[!0-9]*) continue ;; esac
    [ "$_sz" -le 2097152 ] || printf '%s (%s bytes)\n' "$_p" "$_sz"
  done
}

# Pre-flight: refuse before any push. It checks the artifact's tree for
# secret-like content and oversized files, prints the artifact's author so a
# surprising one is visible before it is opened as a change, and (when it will
# push) requires a parseable github.com remote. Exit 8: the remote is not a
# parseable github.com remote. Exit 10: secret-like content, a .env*/*.pem
# file, or a file over 2 MB (unless --allow-large).
preflight() {
  log "artifact $commit author: $(git show -s --format='%an <%ae>' "$commit")"

  _bad=0
  _hits=$(git grep -n -I \
    -e 'ghp_' -e 'github_pat_' -e 'AKIA' \
    -e 'BEGIN RSA PRIVATE KEY' -e 'BEGIN EC PRIVATE KEY' \
    -e 'BEGIN OPENSSH PRIVATE KEY' -e 'sk-' -e 'RAJ_CONTROL_TOKEN=' \
    "$tree" 2>/dev/null | sed "s/^$tree://" || true)
  if [ -n "$_hits" ]; then
    _bad=1
    printf 'publish-single: pre-flight: secret-like content in the artifact being published:\n%s\n' "$_hits" >&2
  fi
  for _p in $(git ls-tree -r --name-only "$tree" 2>/dev/null); do
    case ${_p##*/} in
      .env*|*.pem)
        _bad=1
        printf 'publish-single: pre-flight: secret-bearing file in the artifact being published: %s\n' "$_p" >&2
        ;;
    esac
  done
  if [ "$_bad" = 1 ]; then
    die 10 "refusing to publish the artifact above; remove the hits, or publish elsewhere"
  fi

  if [ "$allow_large" != 1 ]; then
    _large=$(large_files "$tree")
    if [ -n "$_large" ]; then
      printf 'publish-single: pre-flight: files over 2 MB in the artifact being published:\n%s\n' "$_large" >&2
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
    --commit) commit=${2:?--commit needs a sha}; shift 2 ;;
    --base) base=${2:?--base needs a ref}; shift 2 ;;
    --branch) branch=${2:?--branch needs a name}; shift 2 ;;
    --remote) remote=${2:?--remote needs a name}; shift 2 ;;
    --no-push) push=0; pr=0; shift ;;
    --no-pr) pr=0; shift ;;
    --pr-with) pr_with=${2:?--pr-with needs a value}; shift 2 ;;
    --allow-large) allow_large=1; shift ;;
    --dry-run) dry=1; shift ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
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

# The artifact. It must exist and be a commit; the base was chosen by the
# export, and the artifact's parent must be exactly it, so a push can never
# carry a different wave's commits.
[ -n "$commit" ] || die 2 "--commit SHA is required: publish pushes the wave's exported commit, not a commit built here"
git cat-file -e "$commit^{commit}" 2>/dev/null || die 4 "--commit $commit does not resolve to a commit"
commit_sha=$(git rev-parse --verify --quiet "$commit^{commit}") || die 4 "--commit $commit does not resolve to a commit"

# The base: an explicit ref (the export's own parent), else the remote's main,
# else local main. It is the parent the artifact must sit on and the diff base.
if [ -z "$base" ]; then
  if git rev-parse --verify --quiet "refs/remotes/$remote/main" >/dev/null; then
    base="$remote/main"
  else
    base="main"
  fi
fi
base_sha=$(git rev-parse --verify --quiet "$base^{commit}") || die 4 "base $base does not resolve to a commit"

parent_sha=$(git rev-parse --verify --quiet "$commit_sha^" 2>/dev/null || true)
[ "$parent_sha" = "$base_sha" ] || die 13 "commit $commit_sha is not parented on the base $base_sha"

tree=$(git rev-parse --verify --quiet "$commit_sha^{tree}") || die 4 "cannot read the tree of $commit_sha"
base_tree=$(git rev-parse "$base_sha^{tree}")
[ "$tree" != "$base_tree" ] || die 5 "nothing to publish: the artifact's tree matches $base"

# The pull/merge target: the current branch's upstream (Q9 auto-detect), never
# a hardcoded main. A --base given as a branch name (manual use, not a raw
# SHA) supplies its leaf when there is no upstream.
base_branch=""
if _up=$(git rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null) && [ -n "$_up" ]; then
  base_branch=${_up#"$remote"/}
elif [ "$base" != "$base_sha" ]; then
  base_branch=${base#"$remote"/}
fi

[ -n "$branch" ] || branch="raj/publish/$(date +%Y%m%d-%H%M%S)"

# Hard rules, refused before anything is pushed: the branch may not be the
# base, a protected trunk name, or the branch this worktree has checked out.
# Publishing must never move HEAD and must never be a force-push.
case $branch in
  "$base_branch"|main|master)
    die 11 "refusing to publish branch $branch: it is the base or a protected trunk name; pass --branch with a wave branch" ;;
esac
if [ "$(git symbolic-ref --quiet --short HEAD 2>/dev/null || true)" = "$branch" ]; then
  die 11 "refusing to publish branch $branch: it is checked out here; publishing must not move HEAD"
fi

# The remote URL decides the pull-request URL. A remote we cannot parse is not
# fatal here: the push still happens and the URL step reports honestly.
remote_url=$(git remote get-url "$remote" 2>/dev/null) || remote_url=""
push_url=$(git remote get-url --push "$remote" 2>/dev/null || true)
[ -n "$push_url" ] || push_url=$remote_url
remote_host=""
remote_slug=""
if [ -n "$remote_url" ]; then
  if _info=$(parse_remote "$remote_url"); then
    remote_host=${_info%% *}
    remote_slug=${_info#* }
  fi
fi

if [ "$dry" = 1 ]; then
  log "would publish artifact $commit_sha onto $base ($base_sha) as branch $branch"
  log "title: $(git log -1 --format=%s "$commit_sha")"
  git diff --stat "$base_sha" "$tree" | tail -n 25
  if [ "$push" = 1 ]; then
    log "would push $commit_sha to $remote and set its upstream"
    if [ -n "$remote_url" ]; then
      # The artifact already exists, so the dry run hands git the real object
      # and shows git's own answer -- what would be created or updated, or
      # refused as a non-fast-forward -- and never a force.
      printf 'publish-single: git push --dry-run %s %s:refs/heads/%s:\n' "$remote" "$commit_sha" "$branch"
      git push --dry-run --porcelain "$remote" "$commit_sha:refs/heads/$branch" 2>&1 || true
    fi
  fi
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

# The pre-flight sees the artifact's tree, so a secret or an oversized file is
# refused here, before the branch moves or the push exists.
preflight

# Second publish: the state of the branch this publish would move. The remote
# is authoritative when pushing; with --no-push the local ref stands in for it.
local_sha=$(git rev-parse --verify --quiet "refs/heads/$branch" 2>/dev/null || true)
remote_sha=""
if [ "$push" = 1 ]; then
  # The push URL, not the fetch URL: a remote can have a different push target
  # (and the pull-request URL above stays the fetch URL the forge knows).
  remote_sha=$(git ls-remote --heads "$push_url" "refs/heads/$branch" 2>/dev/null | cut -f1)
fi
target_sha=$local_sha
if [ "$push" = 1 ] && [ -n "$remote_sha" ]; then
  target_sha=$remote_sha
fi

if [ -n "$target_sha" ] && [ "$target_sha" = "$commit_sha" ]; then
  # Unchanged artifact: nothing to push. Reprint the URL and move no ref.
  log "nothing to publish: $branch already holds $commit_sha"
  if [ "$pr" = 1 ] && [ "$pr_with" != gh ]; then
    case $remote_host in
      github.com)
        printf 'publish-single: open a pull request: https://github.com/%s/pull/new/%s\n' "$remote_slug" "$branch" ;;
      gitlab.com)
        log "GitLab creates the merge request from the push options; its URL is not constructed here"
        printf 'publish-single: remote: %s\n' "$remote_url" ;;
    esac
  fi
  exit 0
fi

if [ -n "$target_sha" ]; then
  if [ "$push" = 1 ]; then
    # Fast-forward only: fetch the remote commit so the ancestor test has it,
    # then refuse a rewrite rather than force one.
    git fetch --quiet "$push_url" "$branch" 2>/dev/null || true
    if ! git merge-base --is-ancestor "$target_sha" "$commit_sha" 2>/dev/null; then
      die 12 "refusing to publish $branch: $target_sha is not an ancestor of $commit_sha (not a fast-forward); re-propose, or publish a new branch (for example $branch-2)"
    fi
  elif ! git merge-base --is-ancestor "$target_sha" "$commit_sha" 2>/dev/null; then
    die 6 "refusing to publish branch $branch: the local ref diverged (not a fast-forward)"
  fi
fi

# Create the local ref, or move it forward only; it is ours and is not the
# checked-out branch (the safety check refused that above).
if [ "$local_sha" != "$commit_sha" ]; then
  git update-ref "refs/heads/$branch" "$commit_sha"
fi
log "committed $commit_sha on branch $branch (HEAD and index untouched)"

if [ "$push" = 1 ]; then
  # GitLab can create the merge request from the push itself; every other host
  # gets the plain push, and the URL is reported after it. Plain git push, no
  # --force: a non-fast-forward was refused above.
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
    title=$(git log -1 --format=%s "$commit_sha")
    url=$(gh pr create --base "$base_branch" --head "$branch" --title "$title" --body "") || die 9 "gh pr create failed"
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
