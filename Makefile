GO ?= go

.PHONY: help fmt fmt-check vet lint test test-race cover check tidy

help:
	@grep -E '^[a-z-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

fmt: ## Format every module in place
	$(GO) fmt ./...

fmt-check: ## Fail if anything is unformatted (CI runs this; fmt does not)
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

vet: ## Run go vet
	$(GO) vet ./...

lint: fmt-check vet ## Format check, vet, then golangci-lint if it is installed
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed; skipping (CI runs it)"; \
	fi

test: ## Run the suite
	$(GO) test ./...

test-race: ## Run the suite under the race detector
	$(GO) test -race ./...

cover: ## Run the suite with coverage and enforce the gate
	@./scripts/coverage.sh

tidy: ## Tidy the module
	$(GO) mod tidy

check: lint test-race cover ## Everything CI runs
