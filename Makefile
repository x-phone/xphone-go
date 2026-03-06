.PHONY: fmt vet build test test-race lint tidy check install-sipcli

fmt:
	gofmt -s -w .

vet:
	go vet ./...

build:
	go build ./...

test:
	go test ./... -count=1

test-race:
	go test -race ./... -count=1

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

install-sipcli:
	cd examples/sipcli && go build -o $(HOME)/bin/sipcli .

# Full verification gate — run before committing.
check: fmt build test vet test-race
