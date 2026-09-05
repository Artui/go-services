GO ?= go

# Every module in the repo. The kernel is first because the adapters depend on
# it and nothing depends on them.
MODULES := . httpx ginx mcpx adkx aguix conformance example

# Every module is built without the workspace, the two unpublished ones
# included. They carry replace directives rather than published versions, but
# the check still means something for them: that their requires and their
# replaces are coherent on their own. Excluding conformance hid exactly that
# once, because `go mod tidy` run inside the workspace stripped every require
# and nothing local complained.
PUBLISHED := $(MODULES)

.PHONY: help fmt fmt-check vet lint test test-race cover check tidy tidy-check verify-modules check-floors

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
	@for m in $(PUBLISHED); do \
		echo "resolve $$m without the workspace"; \
		(cd $$m && GOWORK=off $(GO) build ./...) || exit 1; \
	done

check-floors: ## Fail if a module's Go floor moved without a decision
	@./scripts/check-go-floors.sh

tidy: ## Tidy every module
	@for m in $(MODULES); do (cd $$m && $(GO) mod tidy); done

# The check half of `tidy`, on the same pattern as fmt / fmt-check: CI runs the
# one that cannot rewrite the tree. Added after an untidy go.mod reached a
# published tag. `go mod tidy` had never run in CI, so nothing noticed that two
# adapters listed the kernel they exist to adapt as `// indirect`, and a Go tag
# cannot be repointed once it is pushed -- so the only thing standing between a
# stale go.mod and permanence was somebody running the mutating target by hand.
# Only stdout is captured, and that is the whole trick: `go mod tidy -diff`
# writes the diff to stdout and its "downloading ..." progress to stderr. Folding
# stderr in passes on a warm module cache and fails on a cold one, which is the
# difference between this laptop and CI -- caught by CI on the first run of this
# very target.
tidy-check: ## Fail if any module is untidy (CI runs this; tidy does not)
	@for m in $(MODULES); do \
		if out="$$(cd $$m && $(GO) mod tidy -diff)"; then :; else \
			if [ -n "$$out" ]; then \
				echo "untidy module: $$m"; echo "$$out"; \
			else \
				echo "go mod tidy -diff failed for $$m (see stderr above)"; \
			fi; \
			exit 1; \
		fi; \
	done

check: lint test-race cover tidy-check verify-modules check-floors ## Everything CI runs
