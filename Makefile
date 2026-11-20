# Ledger developer entry points. Run `make help` for the list.
GO       ?= go
GOLANGCI ?= golangci-lint
BIN_DIR  ?= bin
BINARY   := $(BIN_DIR)/ledger
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X main.version=$(VERSION)

.PHONY: help fmt fmt-check vet lint test build check clean

help: ## list the targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-10s %s\n", $$1, $$2}'

fmt: ## rewrite Go files with gofmt -s
	gofmt -s -w .

fmt-check: ## fail when any Go file is not gofmt -s clean
	@out=$$(gofmt -s -l .); if [ -n "$$out" ]; then echo "not gofmt -s clean:"; echo "$$out"; exit 1; fi

vet: ## go vet
	$(GO) vet ./...

lint: ## golangci-lint with .golangci.yml
	$(GOLANGCI) run ./...

test: ## offline unit tests with the race detector
	$(GO) test -race ./...

build: ## build bin/ledger with the version from git, pure Go (CGO_ENABLED=0)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/ledger
	@$(GO) version -m $(BINARY) | grep -q 'CGO_ENABLED=0' || { echo "$(BINARY) was not built with CGO_ENABLED=0"; exit 1; }

check: fmt-check vet lint test build ## everything the Go checks need, in order

clean: ## remove build output
	rm -rf $(BIN_DIR)
