# -------------------------------------------------------------------------------
# Vagabond - Build, Test, and Quality Gates
#
# Author: Alex Freidah
#
# Common development tasks for the Vagabond control plane and CLI. `make check`
# is the gate a change must pass before it is pushed, and it is what CI runs.
# Every target is safe to run against a tree with no Go files, which matters
# while the contract packages are still being written.
# -------------------------------------------------------------------------------

GO             ?= go
GOBIN          ?= $(shell $(GO) env GOPATH)/bin

GOLANGCI_LINT  := $(GOBIN)/golangci-lint
MOCKGEN        := $(GOBIN)/mockgen

# Pinned so that a lint failure is a code change rather than a tool upgrade.
GOLANGCI_VERSION ?= v2.13.0
MOCKGEN_VERSION  ?= v0.6.0

COVERPROFILE   ?= cover.out

# Go tooling exits non-zero when ./... matches nothing, so every target that
# operates on packages is guarded. The contract packages do not exist yet and
# CI runs on every pull request in the meantime; without this the build is red
# for reasons that have nothing to do with the change under review.
HAVE_GO_PKGS   := [ -n "$$($(GO) list ./... 2>/dev/null)" ]
NO_PKGS_MSG    := no Go packages yet, skipping


# -------------------------------------------------------------------------
# DEFAULT TARGET
# -------------------------------------------------------------------------

help: ## Display available Make targets
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage: make \033[36m<target>\033[0m\n"} \
		/^[a-zA-Z0-9_-]+:.*?## / { \
			gsub(/[A-Z_][A-Z0-9_]*=[a-zA-Z0-9_|-]+/, "\033[33m&\033[0m", $$2); \
			printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2 \
		} \
		/^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0, 5)}' $(MAKEFILE_LIST)

##@ Build

# -------------------------------------------------------------------------
# BUILD
# -------------------------------------------------------------------------

build: ## Build the control plane and CLI
	@if $(HAVE_GO_PKGS); then $(GO) build ./...; else echo "$(NO_PKGS_MSG) build"; fi

##@ Quality

# -------------------------------------------------------------------------
# FORMATTING AND STATIC ANALYSIS
# -------------------------------------------------------------------------

fmt: $(GOLANGCI_LINT) ## Apply the formatting the linter enforces
	$(GOLANGCI_LINT) fmt

fmt-check: $(GOLANGCI_LINT) ## Verify formatting without modifying files
	$(GOLANGCI_LINT) fmt --diff

vet: ## Run Go vet static analysis
	@if $(HAVE_GO_PKGS); then $(GO) vet ./...; else echo "$(NO_PKGS_MSG) vet"; fi

lint: $(GOLANGCI_LINT) ## Run golangci-lint, including the depguard boundaries
	@if $(HAVE_GO_PKGS); then $(GOLANGCI_LINT) run; else echo "$(NO_PKGS_MSG) lint"; fi

# -------------------------------------------------------------------------
# TESTING
# -------------------------------------------------------------------------

test: ## Run Go tests with the race detector
	@if $(HAVE_GO_PKGS); then $(GO) test -race ./...; else echo "$(NO_PKGS_MSG) test"; fi

test-fast: ## Run Go tests without the race detector for quick iteration
	@if $(HAVE_GO_PKGS); then $(GO) test ./...; else echo "$(NO_PKGS_MSG) test"; fi

cover: ## Run tests and report total coverage
	@if $(HAVE_GO_PKGS); then \
		$(GO) test -race -coverprofile=$(COVERPROFILE) -covermode=atomic ./... && \
		$(GO) tool cover -func=$(COVERPROFILE) | tail -1; \
	else echo "$(NO_PKGS_MSG) cover"; fi

# Integration tests are gated behind a build tag and manage their own
# containers through testcontainers, so nothing needs starting by hand. The
# target exists now so that the invocation is settled before Chunk 4 adds the
# first test that needs it.
integration-test: ## Run integration tests (requires Docker)
	@if [ -d internal/integration ]; then \
		$(GO) test -race -tags=integration ./internal/integration/...; \
	else echo "$(NO_PKGS_MSG) integration-test"; fi

check: fmt-check vet lint test ## Everything CI runs

##@ Development

# -------------------------------------------------------------------------
# CODE GENERATION
# -------------------------------------------------------------------------

generate: $(MOCKGEN) ## Generate interface mocks
	@if $(HAVE_GO_PKGS); then $(GO) generate ./...; else echo "$(NO_PKGS_MSG) generate"; fi

# CI runs this to catch a mock that was not regenerated after its interface
# changed, which otherwise surfaces as a confusing compile failure later.
generate-check: generate ## Fail if generated code is out of date
	@if ! git diff --quiet; then \
		echo "error: generated code is out of date, run 'make generate'"; \
		git diff --stat; \
		exit 1; \
	fi

##@ Tools

# -------------------------------------------------------------------------
# TOOL INSTALLATION
# -------------------------------------------------------------------------

tools: ## Install build and lint dependencies into GOPATH/bin
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	$(GO) install go.uber.org/mock/mockgen@$(MOCKGEN_VERSION)

$(GOLANGCI_LINT):
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

$(MOCKGEN):
	$(GO) install go.uber.org/mock/mockgen@$(MOCKGEN_VERSION)

##@ Cleanup

# -------------------------------------------------------------------------
# CLEANUP
# -------------------------------------------------------------------------

clean: ## Remove build and coverage artifacts
	$(GO) clean
	rm -f $(COVERPROFILE)

.PHONY: help build fmt fmt-check vet lint test test-fast cover integration-test check generate generate-check tools clean
