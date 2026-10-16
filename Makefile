.PHONY: test lint

test:
	go test -timeout 30m ./...

lint:
	golangci-lint run
