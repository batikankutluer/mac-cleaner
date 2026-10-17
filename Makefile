BIN     ?= $(HOME)/.local/bin/cleaner
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test

build:
	go build -ldflags "$(LDFLAGS)" -o cleaner .

# Installs the `cleaner` command; ~/.local/bin is on PATH.
install:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

test:
	go vet ./...
	go test ./...
