BINARY  := sticky-converter
CMD     := ./cmd/sticky-converter
VERSION := $(shell cat VERSION 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)"
PREFIX  ?= /usr/local

.PHONY: build install clean test

build:
	go build $(LDFLAGS) -o dist/$(BINARY) $(CMD)

install:
	go build $(LDFLAGS) -o $(PREFIX)/bin/$(BINARY) $(CMD)

clean:
	rm -rf dist/

test:
	go test -race ./...
