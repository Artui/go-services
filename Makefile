GO ?= go

# Every module in the repo. The kernel is first because the adapters depend on
# it and nothing depends on them.
MODULES := . httpx ginx mcpx

.PHONY: help fmt fmt-check vet lint test test-race cover check tidy verify-modules

help:
	@grep -E '^[a-z-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

fmt: ## Format every module in place
	@for m in $(MODULES); do (cd $$m && $(GO) fmt ./...); done

fmt-check: ## Fail if anything is unformatted (CI runs this; fmt does not)
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

vet: ## Run go vet in every module
	@for m in $(MODULES); do echo "vet $$m"; (cd $$m && $(GO) vet ./...) || exit 1; done

lint: fmt-check vet ## Format check, vet, then golangci-lint if it is installed
	@if command -v golangci-lint >/dev/null 2>&1; then \
		for m in $(MODULES); do echo "lint $$m"; (cd $$m && golangci-lint run) || exit 1; done; \
	else \
		echo "golangci-lint not installed; skipping (CI runs it)"; \
	fi

test: ## Run every module's suite
	@for m in $(MODULES); do echo "test $$m"; (cd $$m && $(GO) test ./...) || exit 1; done

test-race: ## Run every module's suite under the race detector
	@for m in $(MODULES); do echo "test $$m"; (cd $$m && $(GO) test -race ./...) || exit 1; done

cover: ## Run coverage and enforce the gate, per module
	@./scripts/coverage.sh

# The workspace file makes a kernel edit visible to every adapter immediately,
# which is exactly what hides a go.mod that no longer stands on its own. This
# target is the honest check, and CI runs it.
verify-modules: ## Prove each module resolves without the workspace
	@for m in $(MODULES); do \
		echo "resolve $$m without the workspace"; \
		(cd $$m && GOWORK=off $(GO) build ./...) || exit 1; \
	done

tidy: ## Tidy every module
	@for m in $(MODULES); do (cd $$m && $(GO) mod tidy); done

check: lint test-race cover verify-modules ## Everything CI runs
