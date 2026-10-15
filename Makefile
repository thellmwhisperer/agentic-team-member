.PHONY: test lint

test:
	go test -timeout 45m ./...

lint:
	golangci-lint run
