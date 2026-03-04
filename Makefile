.PHONY: test test-race lint tidy

test:
	go test ./...

test-race:
	go test -race ./...

lint:
	golangci-lint run ./...

tidy:
	go mod tidy
