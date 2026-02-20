.PHONY: build run test lint tidy install docker

BINARY := sticky-refinery
CMD     := ./cmd/sticky-refinery
PREFIX  ?= /usr/local

build:
	CGO_ENABLED=0 go build -o $(BINARY) $(CMD)

install:
	CGO_ENABLED=0 go build -o $(PREFIX)/bin/$(BINARY) $(CMD)

docker:
	$(MAKE) -C docker build

run: build
	./$(BINARY) -config config.yaml

test:
	go test ./...

lint:
	golangci-lint run ./...

tidy:
	go mod tidy
