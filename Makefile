BINARY := dataporch
BUILD_DIR := bin
GO ?= go
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK ?= govulncheck

.DEFAULT_GOAL := help

.PHONY: audit build check clean fmt fmt-check help lint lint-fix run test test-race tidy tidy-check vet

build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -o $(BUILD_DIR)/$(BINARY) ./cmd/dataporch

run:
	$(GO) run ./cmd/dataporch

test:
	$(GO) test -shuffle=on ./...

test-race:
	$(GO) test -race -shuffle=on ./...

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
		'  check        Run the local quality gate.' \
		'  clean        Remove generated local artifacts.' \
		'  fmt          Format Go source files.' \
		'  fmt-check    Check Go source formatting.' \
		'  lint         Run configured lint checks.' \
		'  lint-fix     Apply safe lint fixes.' \
		'  run          Run DataPorch locally.' \
		'  test         Run unit tests.' \
		'  test-race    Run unit tests with the race detector.' \
		'  tidy         Reconcile module files.' \
		'  tidy-check   Verify module files are tidy.' \
		'  vet          Run the standard Go analyzers.'
