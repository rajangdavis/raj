.PHONY: build check fmt fmt-check vet test race smoke

build:
	go build -o ./bin/ ./cmd/raj

# check is what CI runs, in the order that fails cheapest first: formatting
# before types, types before the suite, the suite before the race detector.
# Keeping the commands here rather than in the workflow means the thing that
# gates a merge is the same thing you can run before pushing.
check: fmt-check vet build test race

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
