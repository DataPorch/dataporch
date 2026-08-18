BINARY := dataporch
BUILD_DIR := bin
GO ?= go
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK ?= govulncheck

.DEFAULT_GOAL := help

.PHONY: audit build build-cgo-disabled check clean fmt fmt-check help lint lint-fix lint-integration run test test-cgo-disabled test-integration test-integration-postgres test-integration-sqlite test-race tidy tidy-check vet

build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -o $(BUILD_DIR)/$(BINARY) ./cmd/dataporch

run:
	$(GO) run ./cmd/dataporch

test:
	$(GO) test -shuffle=on ./...

test-cgo-disabled:
	CGO_ENABLED=0 $(GO) test -shuffle=on ./...

test-race:
	$(GO) test -race -shuffle=on ./...

test-integration: test-integration-postgres test-integration-sqlite

test-integration-postgres:
	$(GO) test -race -count=1 -tags=integration ./internal/connection/postgres
	$(GO) test -race -count=1 -tags=integration -run '^Test.*Postgres.*Integration$$' ./internal/app

test-integration-sqlite:
	$(GO) test -race -count=1 -tags=integration ./internal/connection/sqlite
	$(GO) test -race -count=1 -tags=integration -run '^Test.*SQLite.*Integration$$' ./internal/app

build-cgo-disabled:
	CGO_ENABLED=0 $(GO) build -trimpath ./cmd/dataporch

vet:
	$(GO) vet ./...

fmt:
	gofmt -s -w .

fmt-check:
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		echo "Go files need formatting:"; \
		echo "$$files"; \
		exit 1; \
	fi

lint:
	$(GOLANGCI_LINT) run ./...

lint-integration:
	$(GOLANGCI_LINT) run --build-tags=integration ./...

lint-fix:
	$(GOLANGCI_LINT) run --fix ./...

tidy:
	$(GO) mod tidy

tidy-check:
	$(GO) mod tidy
	@git diff --exit-code -- go.mod go.sum
	@test -z "$$(git status --porcelain -- go.mod go.sum)"

audit:
	$(GOVULNCHECK) ./...

check: fmt-check tidy-check vet test-race lint build

clean:
	@rm -f -- $(BUILD_DIR)/$(BINARY) coverage.out coverage.html
	@rmdir -- $(BUILD_DIR) 2>/dev/null || true

help:
	@printf '%s\n' \
		'Usage: make <target>' \
		'' \
		'Targets:' \
		'  audit        Scan dependencies for known vulnerabilities.' \
		'  build        Build the DataPorch binary.' \
		'  build-cgo-disabled  Build with CGO disabled.' \
		'  check        Run the local quality gate.' \
		'  clean        Remove generated local artifacts.' \
		'  fmt          Format Go source files.' \
		'  fmt-check    Check Go source formatting.' \
		'  lint         Run configured lint checks.' \
		'  lint-fix     Apply safe lint fixes.' \
		'  lint-integration  Lint ordinary and integration-tagged Go files.' \
		'  run          Run DataPorch locally.' \
		'  test-cgo-disabled  Run tests with CGO disabled.' \
		'  test         Run unit tests.' \
		'  test-integration  Run all relational integration tests; PostgreSQL requires DATAPORCH_TEST_POSTGRES_DSN.' \
		'  test-integration-postgres  Run PostgreSQL adapter and PostgreSQL app integration tests.' \
		'  test-integration-sqlite  Run SQLite adapter and SQLite app integration tests.' \
		'  test-race    Run unit tests with the race detector.' \
		'  tidy         Reconcile module files.' \
		'  tidy-check   Verify module files are tidy.' \
		'  vet          Run the standard Go analyzers.'
