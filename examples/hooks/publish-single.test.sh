#!/bin/sh
# publish-single.test.sh — publish-single.sh against a real throwaway repo.
#
# Pins what the script promises: the published commit holds exactly the working
# tree .gitignore allows (new, changed and deleted files), it is parented on the
# base, the new branch exists, and the user's HEAD, index and working tree are
# untouched. Also the refusals: nothing to publish, an existing branch, a base
# that does not resolve, and a workspace raj reports dirty. And the GitHub flow
# with ordinary git: the branch is pushed and the pull/new URL is printed for
# both the ssh and https remote forms, `--no-pr` prints no URL, and a remote
# that is not a parseable github.com URL is refused (exit 8) before the push.
# Also the pre-flight: the author identity is printed, and a planted `ghp_`
# line, a `.env*` file and an oversized file are each refused (exit 10);
# --allow-large lets an oversized file through. Needs only git; `gh` and the
# network are never reached.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
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
printf 'secret\n' > ignored.log
printf '*.log\n' > .gitignore
git add keep.txt gone.txt .gitignore
git commit -q -m base
base=$(git rev-parse HEAD)

# The user's state: an edit, a new file, a deletion, an ignored file, and a
# staged change the script must not disturb.
printf 'keep, edited\n' > keep.txt
printf 'new\n' > new.txt
rm gone.txt
printf 'staged\n' > staged.txt
git add staged.txt
index_before=$(git diff --cached --name-only | sort | tr '\n' ' ')

export RAJ_PUBLISH_SKIP_STATUS=1
sh "$script" --base main --branch raj/publish/test --title "test publish" --no-push >/dev/null

[ "$(git rev-parse HEAD)" = "$base" ] || fail "HEAD moved"
[ "$(git symbolic-ref --short HEAD)" = main ] || fail "HEAD is no longer on main"
[ "$(git diff --cached --name-only | sort | tr '\n' ' ')" = "$index_before" ] || fail "the index changed"
[ -f new.txt ] && [ ! -f gone.txt ] || fail "the working tree changed"

commit=$(git rev-parse --verify raj/publish/test) || fail "branch not created"
[ "$(git rev-parse "$commit^")" = "$base" ] || fail "commit is not parented on the base"
files=$(git ls-tree --name-only "$commit" | sort | tr '\n' ' ')
[ "$files" = ".gitignore keep.txt new.txt staged.txt " ] || fail "published tree = [$files]"
[ "$(git show "$commit:keep.txt")" = "keep, edited" ] || fail "edit not published"
[ "$(git log -1 --format=%s "$commit")" = "test publish" ] || fail "title not used"

# Refusals.
if sh "$script" --base main --branch raj/publish/test --no-push >/dev/null 2>&1; then
  fail "an existing branch was not refused"
else
  [ $? -eq 6 ] || fail "existing branch: wrong exit code"
fi
if sh "$script" --base no-such-ref --no-push >/dev/null 2>&1; then
  fail "an unresolvable base was not refused"
else
  [ $? -eq 4 ] || fail "bad base: wrong exit code"
fi
git stash -q -u
if sh "$script" --base main --no-push >/dev/null 2>&1; then
  fail "a tree equal to the base was not refused"
else
  [ $? -eq 5 ] || fail "nothing to publish: wrong exit code"
fi
git stash pop -q

# A workspace raj reports dirty is refused before anything is built.
stub=$aux/bin
mkdir -p "$stub"
printf '#!/bin/sh\nexit 1\n' > "$stub/raj"
chmod +x "$stub/raj"
unset RAJ_PUBLISH_SKIP_STATUS
if PATH="$stub:$PATH" sh "$script" --base main --branch raj/publish/dirty --no-pr >/dev/null 2>&1; then
  fail "a workspace raj reports dirty was not refused"
else
  [ $? -eq 3 ] || fail "dirty workspace: wrong exit code"
fi
export RAJ_PUBLISH_SKIP_STATUS=1

# A rejected set leaves the buffer dirty after the agreed composition is
# saved: the text stays in the view but not on disk, so a save cannot clean
# it and only clear disposes of it. The refusal must carry status's named
# reason rather than swallow it, so the next step is in the message.
printf '#!/bin/sh\necho "a.go: dirty: 2 rejected sets (clear to dispose)"\nexit 1\n' > "$stub/raj"
chmod +x "$stub/raj"
unset RAJ_PUBLISH_SKIP_STATUS
out=$(PATH="$stub:$PATH" sh "$script" --base main --branch raj/publish/rejected --no-pr 2>&1) && st=0 || st=$?
[ "$st" -eq 3 ] || fail "rejected-set workspace: exit $st (wanted 3)"
case $out in
  *"2 rejected sets (clear to dispose)"*) ;;
  *) fail "rejected-set workspace: status reason not surfaced: [$out]" ;;
esac
export RAJ_PUBLISH_SKIP_STATUS=1

# Dry run writes nothing.
refs_before=$(git for-each-ref | wc -l)
sh "$script" --base main --branch raj/publish/dry --dry-run >/dev/null
[ "$(git for-each-ref | wc -l)" = "$refs_before" ] || fail "dry run wrote a ref"

# The GitHub flow with plain git: push and print the pull/new URL. A local bare
# repo is the push target while the configured remote URL stays the GitHub form,
# so no network is needed and both URL shapes are exercised.
git init -q --bare "$aux/remote.git"
git remote remove origin 2>/dev/null || true
git remote add origin git@github.com:acme/widgets.git
git remote set-url --push origin "$aux/remote.git"

out=$(sh "$script" --base main --branch raj/publish/ssh --title "ssh remote" 2>"$aux/err") && st=0 || st=$?
[ "$st" -eq 0 ] || fail "ssh remote: exit $st ($(cat "$aux/err"))"
case $out in
  *"publish-single: open a pull request: https://github.com/acme/widgets/pull/new/raj/publish/ssh"*) ;;
  *) fail "ssh remote: URL not printed: [$out]" ;;
esac
case $out in
  *"publish-single: author: test <test@example.com>"*) ;;
  *) fail "author identity not printed: [$out]" ;;
esac
[ "$(git --git-dir="$aux/remote.git" rev-parse --verify refs/heads/raj/publish/ssh 2>/dev/null || true)" = "$(git rev-parse raj/publish/ssh)" ] || fail "ssh remote: branch not pushed"

git remote set-url origin https://github.com/acme/gadgets.git
out=$(sh "$script" --base main --branch raj/publish/https --title "https remote" 2>"$aux/err") && st=0 || st=$?
[ "$st" -eq 0 ] || fail "https remote: exit $st ($(cat "$aux/err"))"
case $out in
  *"publish-single: open a pull request: https://github.com/acme/gadgets/pull/new/raj/publish/https"*) ;;
  *) fail "https remote: URL not printed: [$out]" ;;
esac

# A remote that is not github.com is refused before the push, naming it.
git remote set-url origin git@gitlab.com:acme/gadgets.git
out=$(sh "$script" --base main --branch raj/publish/gitlab --title "gitlab remote" 2>&1) && st=0 || st=$?
[ "$st" -eq 8 ] || fail "non-github remote: exit $st (wanted 8)"
case $out in
  *gitlab.com*) ;;
  *) fail "non-github remote: host not named: [$out]" ;;
esac
[ "$(git --git-dir="$aux/remote.git" rev-parse --verify refs/heads/raj/publish/gitlab 2>/dev/null || true)" = "" ] || fail "non-github remote: branch was pushed"
git remote set-url origin https://github.com/acme/gadgets.git

# --no-pr: still pushes, prints no URL and never takes the gh path.
out=$(sh "$script" --base main --branch raj/publish/nopr --title "no pr" --no-pr 2>"$aux/err") && st=0 || st=$?
[ "$st" -eq 0 ] || fail "--no-pr: exit $st ($(cat "$aux/err"))"
case $out in
  *pull/new*|*"open a pull request"*) fail "--no-pr printed a URL: [$out]" ;;
esac
[ "$(git --git-dir="$aux/remote.git" rev-parse --verify refs/heads/raj/publish/nopr 2>/dev/null || true)" = "$(git rev-parse raj/publish/nopr)" ] || fail "--no-pr: branch not pushed"

# An unparseable remote is refused before the push, naming what it saw.
git remote set-url origin "not a url"
out=$(sh "$script" --base main --branch raj/publish/bad --title "bad remote" 2>&1) && st=0 || st=$?
[ "$st" -eq 8 ] || fail "unparseable remote: exit $st (wanted 8)"
case $out in
  *"not a url"*) ;;
  *) fail "unparseable remote: did not name what it saw: [$out]" ;;
esac
[ "$(git --git-dir="$aux/remote.git" rev-parse --verify refs/heads/raj/publish/bad 2>/dev/null || true)" = "" ] || fail "unparseable remote: branch was pushed"

# The pre-flight scans the content about to be committed. A planted `ghp_`
# line refuses (exit 10) and reports its path and line.
printf 'token = ghp_0123456789abcdef\n' > leaked.txt
out=$(sh "$script" --base main --branch raj/publish/secret --title "secret" --no-push 2>&1) && st=0 || st=$?
[ "$st" -eq 10 ] || fail "secret: exit $st (wanted 10)"
case $out in
  *"leaked.txt:1:"*) ;;
  *) fail "secret: hit path/line not reported: [$out]" ;;
esac
[ "$(git rev-parse --verify --quiet refs/heads/raj/publish/secret || true)" = "" ] || fail "secret: a branch was created anyway"
rm leaked.txt

# A `.env*` file is a secret-bearing name and refuses too.
printf 'X=1\n' > .env.test
out=$(sh "$script" --base main --branch raj/publish/env --title "env" --no-push 2>&1) && st=0 || st=$?
[ "$st" -eq 10 ] || fail "dotenv: exit $st (wanted 10)"
case $out in
  *.env.test*) ;;
  *) fail "dotenv: name not reported: [$out]" ;;
esac
rm .env.test

# A file over 2 MB refuses, unless --allow-large.
dd if=/dev/zero of=big.bin bs=1024 count=3072 2>/dev/null
out=$(sh "$script" --base main --branch raj/publish/large --title "large" --no-push 2>&1) && st=0 || st=$?
[ "$st" -eq 10 ] || fail "large file: exit $st (wanted 10)"
case $out in
  *big.bin*) ;;
  *) fail "large file: not named: [$out]" ;;
esac
sh "$script" --base main --branch raj/publish/large-ok --title "large ok" --no-push --allow-large >/dev/null 2>&1 || fail "large file: --allow-large still refused"
rm big.bin

if [ "$fails" -ne 0 ]; then
  printf 'publish-single.test: %d failure(s)\n' "$fails" >&2
  exit 1
fi
printf 'publish-single.test: ok\n'
