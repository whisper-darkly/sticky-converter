.PHONY: build run test lint tidy

BINARY := sticky-refinery
CMD     := ./cmd/sticky-refinery

build:
	CGO_ENABLED=0 go build -o $(BINARY) $(CMD)

run: build
	./$(BINARY) -config config.yaml

test:
	go test ./...

lint:
	golangci-lint run ./...

tidy:
	go mod tidy
