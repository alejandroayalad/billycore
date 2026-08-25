# AGENTS.md §7 — `make check` is what runs before showing work.
.PHONY: check build test vet fmt run clean

check: fmt vet test

build:
	go build -o billycore ./cmd/billycore

test:
	go test ./...

vet:
	go vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

run: build
	./billycore

clean:
	rm -f billycore
