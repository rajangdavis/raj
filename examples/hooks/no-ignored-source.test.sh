#!/bin/sh
# no-ignored-source.test.sh — no-ignored-source.sh against throwaway repos.
#
# Pins what the gate promises: an ignored source-like file that is not on the
# allowlist is named and refused non-zero with the ANCHOR/ADD advisory; a
# source-like path under an allowlisted prefix passes; a repo whose only
# ignored paths are non-source passes. Needs only git.
set -eu

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
script=$here/no-ignored-source.sh
fails=0
fail() { printf 'no-ignored-source.test: FAIL %s\n' "$*" >&2; fails=$((fails + 1)); }

repo=$(mktemp -d "${TMPDIR:-/tmp}/no-ignored-test.XXXXXX")
clean=$repo/clean
hide=$repo/hide
allow=$repo/allow
norepo=$repo/norepo
mkdir -p "$clean" "$hide" "$allow" "$norepo"
trap 'rm -rf "$repo"' EXIT

# A clean repo: the only ignored path is a non-source .log.
cd "$clean"
git init -q -b main
git config user.email test@example.com
git config user.name test
printf '*.log\n' > .gitignore
git add .gitignore
git commit -q -m base
printf 'noise\n' > debug.log
out=$(sh "$script" 2>&1) && st=0 || st=$?
[ "$st" -eq 0 ] || fail "clean repo: exit $st ($out)"
[ -z "$out" ] || fail "clean repo: unexpected output: [$out]"

# A directory that is not a git repo is skipped, not failed: the check hook
# runs make check on a projected tree with no .git, and that must pass.
cd "$norepo"
out=$(sh "$script" 2>&1) && st=0 || st=$?
[ "$st" -eq 0 ] || fail "no-repo dir: exit $st ($out)"
case $out in
  *"not a git work tree, skipped"*) ;;
  *) fail "no-repo dir: skip line missing: [$out]" ;;
esac

# The bug: an unanchored `raj` matches the directory component of cmd/raj/, so

# the real source file cmd/raj/main_test.go is ignored. It must be named and
# refused, with the advisory that says how to fix the pattern.
cd "$hide"
git init -q -b main
git config user.email test@example.com
git config user.name test
printf 'raj\n*.log\n' > .gitignore
git add .gitignore
git commit -q -m base
mkdir -p cmd/raj
printf 'package main\n' > cmd/raj/main_test.go
out=$(sh "$script" 2>&1) && st=0 || st=$?
[ "$st" -ne 0 ] || fail "hidden source: the gate passed a hidden source-like file"
case $out in
  *cmd/raj/main_test.go*) ;;
  *) fail "hidden source: offending path not named: [$out]" ;;
esac
case $out in
  *ANCHOR*ADD*) ;;
  *) fail "hidden source: ANCHOR/ADD advisory missing: [$out]" ;;
esac

# An allowlisted prefix: a generated .go under bin/ is ignored on purpose and
# passes; the gate stays quiet.
cd "$allow"
git init -q -b main
git config user.email test@example.com
git config user.name test
printf 'bin/\n' > .gitignore
git add .gitignore
git commit -q -m base
mkdir -p bin
printf '// generated\n' > bin/gen.go
out=$(sh "$script" 2>&1) && st=0 || st=$?
[ "$st" -eq 0 ] || fail "allowlisted path: exit $st ($out)"
[ -z "$out" ] || fail "allowlisted path: unexpected output: [$out]"

if [ "$fails" -ne 0 ]; then
  printf 'no-ignored-source.test: %d failure(s)\n' "$fails" >&2
  exit 1
fi
printf 'no-ignored-source.test: ok\n'
