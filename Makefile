.PHONY: build check fmt fmt-check vet test race smoke test-times test-pkgs

build:
	go build -o ./bin/ ./cmd/raj

# check is the product gate CI runs, in the order that fails cheapest first:
# formatting before types, types before the suite, the suite before the race
# detector. Keeping the commands here rather than in the workflow means the
# thing that gates a merge is the same thing you can run before pushing.
#
# The dev-process tests (the Claude guard and the cycle test) are not part of
# the product; scripts/raj-cycle.sh step 1 runs them before it calls check.
check: fmt fmt-check vet build test race

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
