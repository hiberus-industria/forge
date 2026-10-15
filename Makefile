# forge — developer tasks. Run `make help` for the list.
SHELL := /bin/sh
GO    ?= go
BIN   := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/hiberus-industria/forge/internal/cli.version=$(VERSION)

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[35m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: generate
generate: ## Regenerate code from api/openapi.yaml
	$(GO) generate ./internal/api

.PHONY: build
build: ## Build bin/forge and bin/forge-api
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/forge ./cmd/forge
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/forge-api ./cmd/forge-api

.PHONY: test
test: ## Run all tests, including testcontainers integration tests (needs Docker)
	$(GO) test -race ./...

.PHONY: test-short
test-short: ## Run unit tests only (skips integration tests)
	$(GO) test -short ./...

.PHONY: lint
lint: ## Run golangci-lint (v2)
	golangci-lint run ./...

.PHONY: fmt
fmt: ## Format code (gofumpt + goimports via golangci-lint)
	golangci-lint fmt ./...

.PHONY: run-api
run-api: ## Start API + Postgres (+ Caddy) with docker compose
	docker compose -f deploy/docker-compose.yml up --build

.PHONY: dev-keycloak
dev-keycloak: ## Start a local Keycloak (http://localhost:8180, realm "hiberus", user dev/dev)
	docker compose -f deploy/keycloak/docker-compose.dev.yml up

.PHONY: demo
demo: ## Open the TUI with demo data (no daemon/API needed)
	FORGE_TUI_DEMO=1 $(GO) run ./cmd/forge tui

.PHONY: snapshot
snapshot: ## Build a local release snapshot with goreleaser
	goreleaser release --snapshot --clean

.PHONY: clean
clean: ## Remove build outputs
	rm -rf $(BIN) dist coverage.out
