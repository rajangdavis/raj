#!/bin/sh
# publish-single.test.sh — publish-single.sh against a real throwaway repo.
#
# The artifact is immutable: the script must push exactly the commit named by
# --commit and never build or publish the working tree. That is the regression
# this pins — the pushed commit used to be built from the saved disk, so a
# worktree that moved after the export could ship unreviewed changes. The
# artifact is built first, the worktree is dirtied afterwards, and the branch
# must still point at the artifact with the extra file absent.
#
# Also pinned: --commit is required (2); a commit that does not exist is
# refused (4); an artifact parented elsewhere is refused (13); an artifact
# whose tree matches the base is refused (5); a planted secret, a `.env*` name
# and an oversized file inside the artifact are each refused (10, with
# --allow-large for the oversized one); a rerun is a no-op; and a diverged
# local branch is refused (6 with --no-push). And the GitHub flow with ordinary
# git: the branch is pushed and the pull/new URL is printed for both the ssh
# and https remote forms, `--no-pr` prints no URL, and a remote that is not a
# parseable github.com URL is refused (exit 8) before the push. Needs only
# git; `gh` and the network are never reached.
set -eu

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
script=$here/publish-single.sh
fails=0
fail() { printf 'publish-single.test: FAIL %s\n' "$*" >&2; fails=$((fails + 1)); }

repo=$(mktemp -d "${TMPDIR:-/tmp}/publish-single-test.XXXXXX")
aux=$(mktemp -d "${TMPDIR:-/tmp}/publish-single-aux.XXXXXX")
trap 'rm -rf "$repo" "$aux"' EXIT
cd "$repo"
git init -q -b main
git config user.email test@example.com
git config user.name test
printf 'keep\n' > keep.txt
printf 'gone\n' > gone.txt
printf '*.log\n' > .gitignore
git add keep.txt gone.txt .gitignore
git commit -q -m base
base=$(git rev-parse HEAD)

# build_artifact PATH builds a commit off the base whose tree is the base tree
# plus PATH's current worktree content, and prints its sha. It writes objects
# only; no ref moves. It is the S3 export in miniature.
build_artifact() {
  _p=$1
  _idx=$(mktemp "${TMPDIR:-/tmp}/publish-single-art.XXXXXX")
  GIT_INDEX_FILE=$_idx git read-tree "$base"
  _blob=$(git hash-object -w --stdin < "$_p")
  GIT_INDEX_FILE=$_idx git update-index --add --cacheinfo "100644,$_blob,$_p"
  _tree=$(GIT_INDEX_FILE=$_idx git write-tree)
  rm -f "$_idx"
  git commit-tree "$_tree" -p "$base" -m "wave artifact"
}

# The artifact exists first; the worktree is dirtied only afterwards. A script
# that rebuilt from disk would push extra.txt; this one must not.
printf 'artifact\n' > artifact.txt
artifact=$(build_artifact artifact.txt)
printf 'extra, never reviewed\n' > extra.txt

# --commit is required: there is no commit to build here any more. The exit is
# 2 (usage), not a run against the worktree.
out=$(sh "$script" --base "$base" --branch raj/publish/nocommit --no-push 2>&1) && st=0 || st=$?
[ "$st" -eq 2 ] || fail "--commit missing: exit $st (wanted 2)"
case $out in
  *--commit*) ;;
  *) fail "--commit missing: reason not named: [$out]" ;;
esac

# A commit that does not exist is refused (4), before any ref moves.
out=$(sh "$script" --commit 0000000000000000000000000000000000000000 --base "$base" --branch raj/publish/badsha --no-push 2>&1) && st=0 || st=$?
[ "$st" -eq 4 ] || fail "bad sha: exit $st (wanted 4)"

# The regression: the branch points at exactly the artifact, and the file added
# to the worktree after the export is not in it. The user's HEAD is untouched.
sh "$script" --commit "$artifact" --base "$base" --branch raj/publish/artifact --no-push >/dev/null
[ "$(git rev-parse raj/publish/artifact)" = "$artifact" ] || fail "branch is not the artifact"
git cat-file -e "$artifact:artifact.txt" 2>/dev/null || fail "artifact.txt missing from the artifact"
if git cat-file -e "$artifact:extra.txt" 2>/dev/null; then fail "extra.txt leaked into the published commit"; fi
[ "$(git rev-parse HEAD)" = "$base" ] || fail "HEAD moved"
[ "$(git symbolic-ref --short HEAD)" = main ] || fail "HEAD is no longer on main"

# A rerun of the unchanged artifact is a no-op: the branch already holds it.
before=$(git rev-parse raj/publish/artifact)
out=$(sh "$script" --commit "$artifact" --base "$base" --branch raj/publish/artifact --no-push 2>&1) && st=0 || st=$?
[ "$st" -eq 0 ] || fail "rerun: exit $st ($out)"
case $out in
  *"nothing to publish"*) ;;
  *) fail "rerun: no no-op note: [$out]" ;;
esac
[ "$(git rev-parse raj/publish/artifact)" = "$before" ] || fail "rerun moved the branch"

# An artifact parented on something other than the base is refused (13): a push
# must never carry another wave's commits.
git commit -q --allow-empty -m other
other=$(git rev-parse HEAD)
elsewhere=$(git commit-tree "$(git rev-parse "$artifact^{tree}")" -p "$other" -m "elsewhere")
out=$(sh "$script" --commit "$elsewhere" --base "$base" --branch raj/publish/elsewhere --no-push 2>&1) && st=0 || st=$?
[ "$st" -eq 13 ] || fail "artifact parented elsewhere: exit $st (wanted 13)"
case $out in
  *"not parented on the base"*) ;;
  *) fail "artifact parented elsewhere: reason missing: [$out]" ;;
esac
[ "$(git rev-parse --verify --quiet refs/heads/raj/publish/elsewhere || true)" = "" ] || fail "elsewhere: a branch was created anyway"

# An artifact whose tree equals the base is nothing to publish (5).
same=$(git commit-tree "$(git rev-parse "$base^{tree}")" -p "$base" -m "same tree")
out=$(sh "$script" --commit "$same" --base "$base" --branch raj/publish/same --no-push 2>&1) && st=0 || st=$?
[ "$st" -eq 5 ] || fail "tree equals base: exit $st (wanted 5)"

# Hard rules: the branch may not be a protected trunk name.
if sh "$script" --commit "$artifact" --base "$base" --branch main --no-push >/dev/null 2>&1; then
  fail "branch == main was not refused"
else
  [ $? -eq 11 ] || fail "branch == main: wrong exit code"
fi
if sh "$script" --commit "$artifact" --base "$base" --branch master --no-push >/dev/null 2>&1; then
  fail "a protected branch name was not refused"
else
  [ $? -eq 11 ] || fail "protected branch: wrong exit code"
fi

# A non-fast-forward on the local branch is refused (6), never forced.
sib=$(git commit-tree "$(git rev-parse "$base^{tree}")" -p "$base" -m sibling)
git update-ref refs/heads/raj/publish/nonff "$sib"
out=$(sh "$script" --commit "$artifact" --base "$base" --branch raj/publish/nonff --no-push 2>&1) && st=0 || st=$?
[ "$st" -eq 6 ] || fail "local divergence: exit $st (wanted 6)"
[ "$(git rev-parse raj/publish/nonff)" = "$sib" ] || fail "local divergence: the branch moved anyway"

# The pre-flight scans the artifact's tree, not the worktree. A planted `ghp_`
# line refuses (exit 10) and reports its path and line; no branch is created.
printf 'token = ghp_0123456789abcdef\n' > leaked.txt
leak=$(build_artifact leaked.txt)
rm leaked.txt
out=$(sh "$script" --commit "$leak" --base "$base" --branch raj/publish/secret --no-push 2>&1) && st=0 || st=$?
[ "$st" -eq 10 ] || fail "secret: exit $st (wanted 10)"
case $out in
  *"leaked.txt:1:"*) ;;
  *) fail "secret: hit path/line not reported: [$out]" ;;
esac
[ "$(git rev-parse --verify --quiet refs/heads/raj/publish/secret || true)" = "" ] || fail "secret: a branch was created anyway"

# A `.env*` name in the artifact is a secret-bearing file and refuses too.
printf 'X=1\n' > .env.test
envart=$(build_artifact .env.test)
rm .env.test
out=$(sh "$script" --commit "$envart" --base "$base" --branch raj/publish/env --no-push 2>&1) && st=0 || st=$?
[ "$st" -eq 10 ] || fail "dotenv: exit $st (wanted 10)"
case $out in
  *.env.test*) ;;
  *) fail "dotenv: name not reported: [$out]" ;;
esac

# A file over 2 MB in the artifact refuses, unless --allow-large.
dd if=/dev/zero of=big.bin bs=1024 count=3072 2>/dev/null
big=$(build_artifact big.bin)
rm big.bin
out=$(sh "$script" --commit "$big" --base "$base" --branch raj/publish/large --no-push 2>&1) && st=0 || st=$?
[ "$st" -eq 10 ] || fail "large file: exit $st (wanted 10)"
case $out in
  *big.bin*) ;;
  *) fail "large file: not named: [$out]" ;;
esac
sh "$script" --commit "$big" --base "$base" --branch raj/publish/large-ok --no-push --allow-large >/dev/null 2>&1 || fail "large file: --allow-large still refused"

# Dry run writes nothing.
refs_before=$(git for-each-ref | wc -l)
sh "$script" --commit "$artifact" --base "$base" --branch raj/publish/dry --dry-run >/dev/null
[ "$(git for-each-ref | wc -l)" = "$refs_before" ] || fail "dry run wrote a ref"

# The GitHub flow with plain git: push the artifact and print the pull/new URL.
# A local bare repo is the push target while the configured remote URL stays the
# GitHub form, so no network is needed and both URL shapes are exercised.
git init -q --bare "$aux/remote.git"
git remote remove origin 2>/dev/null || true
git remote add origin git@github.com:acme/widgets.git
git remote set-url --push origin "$aux/remote.git"

# The dry run carries git's own push answer inline, so a proposal can pin it.
out=$(sh "$script" --commit "$artifact" --base "$base" --branch raj/publish/drypush --dry-run 2>&1) && st=0 || st=$?
[ "$st" -eq 0 ] || fail "dry run with a remote: exit $st"
case $out in
  *"git push --dry-run"*) ;;
  *) fail "dry run: git push --dry-run output missing: [$out]" ;;
esac

out=$(sh "$script" --commit "$artifact" --base "$base" --branch raj/publish/ssh 2>"$aux/err") && st=0 || st=$?
[ "$st" -eq 0 ] || fail "ssh remote: exit $st ($(cat "$aux/err"))"
case $out in
  *"publish-single: open a pull request: https://github.com/acme/widgets/pull/new/raj/publish/ssh"*) ;;
  *) fail "ssh remote: URL not printed: [$out]" ;;
esac
[ "$(git --git-dir="$aux/remote.git" rev-parse --verify refs/heads/raj/publish/ssh 2>/dev/null || true)" = "$artifact" ] || fail "ssh remote: the artifact was not pushed"

# A second publish of the unchanged artifact is a no-op that reprints the URL
# and moves nothing.
out=$(sh "$script" --commit "$artifact" --base "$base" --branch raj/publish/ssh 2>"$aux/err2") && st=0 || st=$?
[ "$st" -eq 0 ] || fail "no-op republish: exit $st ($(cat "$aux/err2"))"
case $out in
  *"nothing to publish"*) ;;
  *) fail "no-op republish: no no-op note: [$out]" ;;
esac
case $out in
  *"https://github.com/acme/widgets/pull/new/raj/publish/ssh"*) ;;
  *) fail "no-op republish: URL not reprinted: [$out]" ;;
esac

git remote set-url origin https://github.com/acme/gadgets.git
out=$(sh "$script" --commit "$artifact" --base "$base" --branch raj/publish/https 2>"$aux/err") && st=0 || st=$?
[ "$st" -eq 0 ] || fail "https remote: exit $st ($(cat "$aux/err"))"
case $out in
  *"publish-single: open a pull request: https://github.com/acme/gadgets/pull/new/raj/publish/https"*) ;;
  *) fail "https remote: URL not printed: [$out]" ;;
esac

# A remote that is not github.com is refused before the push, naming it.
git remote set-url origin git@gitlab.com:acme/gadgets.git
out=$(sh "$script" --commit "$artifact" --base "$base" --branch raj/publish/gitlab 2>&1) && st=0 || st=$?
[ "$st" -eq 8 ] || fail "non-github remote: exit $st (wanted 8)"
case $out in
  *gitlab.com*) ;;
  *) fail "non-github remote: host not named: [$out]" ;;
esac
[ "$(git --git-dir="$aux/remote.git" rev-parse --verify refs/heads/raj/publish/gitlab 2>/dev/null || true)" = "" ] || fail "non-github remote: branch was pushed"
git remote set-url origin https://github.com/acme/gadgets.git

# --no-pr: still pushes, prints no URL and never takes the gh path.
out=$(sh "$script" --commit "$artifact" --base "$base" --branch raj/publish/nopr --no-pr 2>"$aux/err") && st=0 || st=$?
[ "$st" -eq 0 ] || fail "--no-pr: exit $st ($(cat "$aux/err"))"
case $out in
  *pull/new*|*"open a pull request"*) fail "--no-pr printed a URL: [$out]" ;;
esac
[ "$(git --git-dir="$aux/remote.git" rev-parse --verify refs/heads/raj/publish/nopr 2>/dev/null || true)" = "$artifact" ] || fail "--no-pr: artifact not pushed"

# An unparseable remote is refused before the push, naming what it saw.
git remote set-url origin "not a url"
out=$(sh "$script" --commit "$artifact" --base "$base" --branch raj/publish/bad 2>&1) && st=0 || st=$?
[ "$st" -eq 8 ] || fail "unparseable remote: exit $st (wanted 8)"
case $out in
  *"not a url"*) ;;
  *) fail "unparseable remote: did not name what it saw: [$out]" ;;
esac
[ "$(git --git-dir="$aux/remote.git" rev-parse --verify refs/heads/raj/publish/bad 2>/dev/null || true)" = "" ] || fail "unparseable remote: branch was pushed"

# --help prints the header comment, not the script body: the header block ends
# at line 40, so the sed range must not run on to `set -eu`.
out=$(sh "$script" --help 2>&1) && st=0 || st=$?
[ "$st" -eq 0 ] || fail "--help: exit $st"
case $out in
  *"set -eu"*) fail "--help leaked the script body" ;;
  *"--commit SHA"*) ;;
  *) fail "--help did not print the usage" ;;
esac

if [ "$fails" -ne 0 ]; then
  printf 'publish-single.test: %d failure(s)\n' "$fails" >&2
  exit 1
fi
printf 'publish-single.test: ok\n'
