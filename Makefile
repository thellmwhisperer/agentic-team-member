.PHONY: test lint release release-check

# ponytail: GoReleaser is pinned here; bump it when a newer release is wanted.
GORELEASER = go run github.com/goreleaser/goreleaser/v2@v2.18.2

test:
	go test -timeout 30m ./...

lint:
	golangci-lint run

release:
	$(GORELEASER) release --clean

release-check:
	$(GORELEASER) release --snapshot --clean
