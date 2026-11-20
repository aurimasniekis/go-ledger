# Ledger developer entry points. Run `make help` for the list.
GO       ?= go
GOLANGCI ?= golangci-lint
BIN_DIR  ?= bin
BINARY   := $(BIN_DIR)/ledger
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X main.version=$(VERSION)

.PHONY: help web-install web-lint web-typecheck web-test web-build web-check build-full e2e fmt fmt-check vet lint test test-nocgo build config-examples workflow-policy check clean

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

test-nocgo: ## run the store tests with CGO_ENABLED=0 (pure-Go SQLite driver, ADR-0003)
	CGO_ENABLED=0 $(GO) test -count=1 ./internal/store/...

build: ## build every package and bin/ledger with the version from git, pure Go (CGO_ENABLED=0)
	CGO_ENABLED=0 $(GO) build ./...
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/ledger
	@$(GO) version -m $(BINARY) | grep -q 'CGO_ENABLED=0' || { echo "$(BINARY) was not built with CGO_ENABLED=0"; exit 1; }

config-examples: ## regenerate config.example.yaml and .env.example from the config definition
	$(GO) test ./internal/config -run TestExamplesInSync -update

workflow-policy: ## workflow permission policy: the negative fixtures must be rejected, then .github/workflows must pass
	sh scripts/check-workflow-permissions.sh --self-test
	sh scripts/check-workflow-permissions.sh .github/workflows

web-install: ## install frontend dependencies from the lockfile (pnpm)
	cd web && pnpm install --frozen-lockfile

web-lint: ## eslint
	cd web && pnpm lint

web-typecheck: ## tsc --noEmit
	cd web && pnpm typecheck

web-test: ## Vitest and Testing Library
	cd web && pnpm test

web-build: ## build the frontend into internal/web/dist (embedded by the Go binary)
	cd web && pnpm build

web-check: web-lint web-typecheck web-test web-build ## every frontend check, then the production build

build-full: web-build build ## build the frontend, then bin/ledger with it embedded

e2e: build-full ## Playwright against the built binary (needs: pnpm exec playwright install chromium)
	cd web && E2E_LEDGER_BIN=../$(BINARY) pnpm test:e2e

check: fmt-check vet lint test test-nocgo web-check build workflow-policy ## everything the checks need, in order

clean: ## remove build output
	rm -rf $(BIN_DIR)
