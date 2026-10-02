.PHONY: build check fmt fmt-check sh-parse shellcheck ignored-source vet test race smoke test-times test-pkgs cyclo dupl quality vuln

# VERSION is the version a built binary reports. The link line below stamps it
# into control's srcVersion, the variable the running editor puts on every
# control response; the vcs.revision the go tool embeds is only the fallback,
# and it is absent on the projected tree the gate builds because that tree has
# no .git. Pass VERSION where the binary will be shipped or run outside a
# checkout:
#
#	make build VERSION=v1.2.3
#
# With VERSION empty the link line is unchanged and a checkout build names its
# revision. There is deliberately no git call here, for the same reason
# sh-parse has none: the build must not need .git.
VERSION ?=
VERSION_LDFLAGS = $(if $(VERSION),-ldflags "-X raj/internal/control.srcVersion=$(VERSION)")

build:
	go build $(VERSION_LDFLAGS) -o ./bin/ ./cmd/raj

# check is the product gate CI runs, in the order that fails cheapest first:
# formatting before types, types before the suite, the suite before the race
# detector. Keeping the commands here rather than in the workflow means the
# thing that gates a merge is the same thing you can run before pushing.
#
# The dev-process tests (the Claude guard and the cycle test) are not part of
# the product; scripts/raj-cycle.sh step 1 runs them before it calls check.
check: fmt fmt-check sh-parse shellcheck ignored-source vet build test race

fmt:
	gofmt -w cmd internal

# gofmt -l prints what is misformatted and exits 0 either way, so the failure
# has to be manufactured from its output.
fmt-check:
	@out="$$(gofmt -l cmd internal)"; \
	if [ -n "$$out" ]; then \
		echo "gofmt needed on:"; echo "$$out"; \
		echo "run: make fmt"; \
		exit 1; \
	fi

# sh-parse parses every tracked shell script with the interpreter it names:
# bash when the first line is a "# shellcheck shell=bash" directive or a bash
# shebang, and the host /bin/sh otherwise. It is a CI step too, so the runner
# /bin/sh (dash) supplies that side of the check; the host run still supplies
# the other, because macOS sh is bash 3.2, and a construct it rejects is one a
# dash-only run would accept.
sh-parse:
	@n=0; \
	for f in $$(git ls-files '*.sh'); do \
		[ -f "$$f" ] || continue; first=""; IFS= read -r first < "$$f" || true; \
		case "$$first" in \
			'# shellcheck shell=bash'*|'#!'*bash*) interp=bash ;; \
			*) interp=sh ;; \
		esac; \
		"$$interp" -n "$$f" || { echo "$$interp -n: parse error in $$f"; exit 1; }; \
		n=$$((n+1)); \
	done; \
	echo "sh-parse: $$n tracked shell script(s) parse"

# shellcheck lints the same tracked shell scripts at warning severity. -S
# warning on purpose: style and info are left out, so the first landing is not
# noisy. shellcheck is a tool the host may not have; when it is absent the gate
# prints one line and skips, the way the repo's other tool-dependent scripts
# behave. CI's runner image ships shellcheck, so the gate always runs there.
shellcheck:
	@files="$$(git ls-files '*.sh' | while IFS= read -r f; do [ -f "$$f" ] && echo "$$f"; done)"; \
	if [ -z "$$files" ]; then echo "shellcheck: no tracked shell scripts"; exit 0; fi; \
	if ! command -v shellcheck >/dev/null 2>&1; then \
		echo "shellcheck: not on PATH, skipping lint (install it: brew install shellcheck)"; \
		exit 0; \
	fi; \
	shellcheck -S warning $$files

# ignored-source refuses an ignored source-like path that is not deliberately
# allowed (examples/hooks/no-ignored-source.sh). It is in check because the bug
# it catches is a .gitignore edit, which no other hook run has to follow: CI
# must see it too.
ignored-source:
	sh examples/hooks/no-ignored-source.sh

vet:
	go vet ./...

test:
	go test ./...

# Separate from test because the editor is threaded in ways that are easy to
# get wrong — the decoder, the tokeniser, the search walk and the control
# server all touch state the event loop owns — and a data race there corrupts
# a document rather than crashing honestly.
race:
	go test -race ./...

# smoke drives the real binary, in a real process, on a real pty, and observes
# it over the control socket. Deliberately outside `check`: it builds a binary,
# spawns processes and waits on wall-clock time, so it is slower and more
# fragile than a unit test by construction. Run it before tagging, not on every
# commit.
#
# Behind a build tag rather than a -run filter so `go test ./...` cannot pick it
# up by accident.
smoke:
	go test -tags smoke -count=1 ./internal/smoke/

# test-times prints the slowest tests in one package, slowest first, so a
# 30-second package reads as a few spikes or a broad serial load. Defaults to
# internal/app; PACKAGE=./internal/control make test-times for another.
PACKAGE ?= ./internal/app
test-times:
	go test -v $(PACKAGE) 2>&1 | grep -E '^--- (PASS|FAIL)' | sort -t'(' -k2 -rn | head -30

# test-pkgs prints the per-package wall time across the repo, slowest first.
test-pkgs:
	go test ./... 2>&1 | grep -E '^(ok|FAIL)' | sort -k3 -rn

# cyclo and dupl are the two analyzers: one threshold each, applied to every
# function and every non-test file. FAIL=yes turns findings into a non-zero
# exit; FAIL=no, the default, prints them and exits 0.
#
# Failing is off by default because the tree does not yet pass a single
# threshold: four functions sit at 115-249, so a gate at any of these values
# would enforce nothing. The fixes (A, B, C, T1, T2, T3 and Q) are what let it
# come on, and the threshold comes down as the maximum falls. There is no
# exemption list and no "for now" allowance: the report names every function
# over the threshold.
#
# Both analyzers run on production code only. gocyclo has -ignore for the file
# path; dupl has no ignore flag, so it is fed the module's non-test files on
# stdin with -files: .GoFiles is exactly the package's non-test Go sources, and
# test scaffolding is not what these gates protect.
#
# The tool versions are pinned here rather than in go.mod: `go run
# <pkg>@<version>` resolves outside this module's requirements, so a
# report-only analyzer adds no dependency to the product build. The first run
# needs the module proxy; the module cache serves it after that.
#
# Both tools take paths, not Go package patterns: each argument is a file or a
# directory, and a directory is searched recursively, so the targets pass `.`
# rather than `./...`. Nothing here needs .git, which is also why `git ls-files`
# stays out - sh-parse no-ops on the projected tree for the same reason.
GOCYCLO ?= go run github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0
DUPL ?= go run github.com/mibk/dupl@v1.1.0
GOVULNCHECK ?= go run golang.org/x/vuln/cmd/govulncheck@v1.7.0
# go list, not git ls-files: the gate also runs on a projected tree with no
# .git, the same reason sh-parse no-ops there.
DUPL_FILES = go list -f '{{$$d := .Dir}}{{range .GoFiles}}{{$$d}}/{{.}}{{"\n"}}{{end}}' ./...

# CYCLO_OVER is the complexity a function must exceed to be listed; it is low on
# purpose so `make quality` keeps the next targets visible. DUPL_THRESHOLD is
# the token count a clone group must reach. Both must be a positive integer.
CYCLO_OVER ?= 20
DUPL_THRESHOLD ?= 150
# FAIL turns the analyzers from a report into a gate: no prints and exits 0, yes
# exits non-zero on findings. It defaults to no because the tree does not pass a
# single threshold yet (see the note above); there is no exemption list.
FAIL ?= no

# cyclo lists every function over CYCLO_OVER, worst first, `_test.go` ignored.
# The parameter check runs first so a bad value fails with a clear message and
# exit 2 before the tool is resolved. With FAIL=no gocyclo's exit status is
# echoed and ignored; with FAIL=yes it is the target's status.
cyclo:
	@case "$(CYCLO_OVER)" in ''|0|*[!0-9]*) echo "cyclo: CYCLO_OVER must be a positive integer, got '$(CYCLO_OVER)'"; exit 2;; esac; \
	case "$(FAIL)" in no|yes) ;; *) echo "cyclo: FAIL must be no or yes, got '$(FAIL)'"; exit 2;; esac; \
	echo "cyclo: gocyclo -over $(CYCLO_OVER) -ignore '_test\.go' -avg . (FAIL=$(FAIL))"; \
	$(GOCYCLO) -over $(CYCLO_OVER) -ignore '_test\.go' -avg .; rc=$$?; \
	echo "cyclo: exit $$rc"; \
	if [ "$(FAIL)" = yes ]; then exit $$rc; fi; \
	exit 0

# dupl lists clone groups of at least DUPL_THRESHOLD tokens over the module's
# non-test files. dupl never exits non-zero on findings (os.Exit appears only in
# usage()), so FAIL=yes reads its "Found total 0 clone groups." footer; a
# non-empty file list is required too, so a failed `go list` cannot read as a
# pass. With FAIL=no the footer is printed and ignored.
dupl:
	@case "$(DUPL_THRESHOLD)" in ''|0|*[!0-9]*) echo "dupl: DUPL_THRESHOLD must be a positive integer, got '$(DUPL_THRESHOLD)'"; exit 2;; esac; \
	case "$(FAIL)" in no|yes) ;; *) echo "dupl: FAIL must be no or yes, got '$(FAIL)'"; exit 2;; esac; \
	echo "dupl: dupl -threshold $(DUPL_THRESHOLD) -files (non-test, FAIL=$(FAIL))"; \
	files="$$($(DUPL_FILES))"; \
	if [ "$(FAIL)" = yes ] && [ -z "$$files" ]; then echo "dupl: go list resolved no files - refusing to pass"; exit 1; fi; \
	out="$$(printf '%s\n' "$$files" | $(DUPL) -files -threshold $(DUPL_THRESHOLD))"; rc=$$?; \
	printf '%s\n' "$$out"; echo "dupl: exit $$rc"; \
	if [ "$(FAIL)" = yes ]; then printf '%s\n' "$$out" | grep -q '^Found total 0 clone groups\.' || { echo "dupl: clone groups at or above $(DUPL_THRESHOLD) tokens (or the analysis failed) - see above"; exit 1; }; fi; \
	exit 0

# quality is the report: both analyzers, one command.
quality: cyclo dupl

# vuln runs govulncheck over the module. It is deliberately not in check: the
# pinned tool comes from the module proxy and the vulnerability database from
# vuln.go.dev, so it needs the network, which the sandbox and the projected
# tree do not have. CI has both, so it runs there as its own step. Pinned here,
# like gocyclo and dupl, so `go run <pkg>@<version>` adds nothing to the product
# build; v1.7.0 rather than the latest because its go directive is 1.25.0, the
# version CI pins (v1.8.0 needs Go 1.26 and CI runs GOTOOLCHAIN=local).
vuln:
	$(GOVULNCHECK) ./...
