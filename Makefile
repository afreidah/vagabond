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

# Prefer a golangci-lint already on PATH over installing another copy. CI
# installs it from a release binary and then runs these same targets, so the
# linter a pull request is judged by is the one a developer ran locally.
GOLANGCI_LINT  := $(shell command -v golangci-lint 2>/dev/null || echo $(GOBIN)/golangci-lint)
MOCKGEN        := $(shell command -v mockgen 2>/dev/null || echo $(GOBIN)/mockgen)

# Pinned so that a lint failure is a code change rather than a tool upgrade.
GOLANGCI_VERSION ?= v2.13.0
MOCKGEN_VERSION  ?= v0.6.0

# Injected into internal/version at link time, so a built binary reports what it
# actually is rather than whatever string was last committed. Read from
# .version, which CI requires a pull request to bump when it changes Go, SQL or
# the site; dev when the file is missing.
VERSION        ?= $(shell cat .version 2>/dev/null || echo dev)
COMMIT         ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
VERSION_PKG    := github.com/afreidah/vagabond/internal/version
GO_LDFLAGS     := -s -w \
	-X $(VERSION_PKG).Version=$(VERSION) \
	-X $(VERSION_PKG).Commit=$(COMMIT)

# Go tooling exits non-zero when ./... matches nothing, so every target that
# operates on packages is guarded. The contract packages do not exist yet and
# CI runs on every pull request in the meantime; without this the build is red
# for reasons that have nothing to do with the change under review.
HAVE_GO_PKGS   := [ -n "$$($(GO) list ./... 2>/dev/null)" ]
NO_PKGS_MSG    := no Go packages yet, skipping

# Images go to $(DOCKER_REGISTRY). The placeholder default means a fork never
# publishes anywhere real by accident.
REGISTRY       ?= $(or $(DOCKER_REGISTRY),registry.example.com)
IMAGE          := $(REGISTRY)/vagabond
WEB_IMAGE      := $(REGISTRY)/vagabond-web
WEB_TAG        ?= $(VERSION)
PLATFORMS      := linux/amd64,linux/arm64


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

# Stamps the version, so a binary built any other way reports itself as a
# development build rather than claiming a release it is not. Nothing else
# compiles for its own sake: vet and test both build every package already.
build: ## Build the vagabond binary with version information
	@if [ -d cmd/vagabond ]; then \
		$(GO) build -ldflags "$(GO_LDFLAGS)" -o vagabond ./cmd/vagabond; \
		echo "built ./vagabond $(VERSION)"; \
	else echo "$(NO_PKGS_MSG) build"; fi

##@ Images

# -------------------------------------------------------------------------
# IMAGES
# -------------------------------------------------------------------------

builder: ## Ensure the buildx builder exists
	@docker buildx inspect vagabond-builder >/dev/null 2>&1 || \
		docker buildx create --name vagabond-builder --driver-opt network=host --use
	@docker buildx inspect --bootstrap

docker: ## Build the vagabond image for the local architecture
	docker build --pull --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-t $(IMAGE):$(VERSION) .

scan: docker ## Scan the local vagabond image with Trivy
	trivy image --severity CRITICAL,HIGH $(IMAGE):$(VERSION)

# Tagged with the version and with latest, so a deployment can pin one or
# follow the other.
push: builder ## Build and push the multi-arch vagabond image
	docker buildx build --pull --platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-t $(IMAGE):$(VERSION) -t $(IMAGE):latest --output type=image,push=true .

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

# Integration tests are gated behind a build tag and manage their own
# containers through testcontainers, so nothing needs starting by hand.
#
# Repo-wide rather than one directory: these live beside the code they cover, so
# the store's tests are in internal/state/postgres. The build tag is what marks
# them, not their location.
integration-test: ## Run integration tests (requires Docker)
	$(GO) test -race -tags=integration -timeout 10m ./...

# -------------------------------------------------------------------------
# COVERAGE
# -------------------------------------------------------------------------

# The profiles CI uploads and SonarCloud combines. -coverpkg=./... counts code
# a test exercises in any package, not only its own, which is how an
# integration test through the server covers dispatch and the stores; atomic
# is what -race requires.
COVER_FLAGS := -race -covermode=atomic -coverpkg=./...

coverage: ## Write coverage.out from the unit tests
	$(GO) test $(COVER_FLAGS) -coverprofile=coverage.out ./...

integration-coverage: ## Write integration-coverage.out from the integration tests (requires Docker)
	$(GO) test $(COVER_FLAGS) -tags=integration -timeout 10m -coverprofile=integration-coverage.out ./...

# The executor drives the host's containerd, which needs root. Built as the
# user and run with sudo, so the build cache stays the user's.
containerd-test: ## Run the containerd executor tests (requires root and containerd)
	$(GO) test -c -race -tags=containerd -o bin/executor.test ./internal/agent/executor
	sudo ./bin/executor.test -test.v -test.timeout 5m

# -------------------------------------------------------------------------
# SECURITY
# -------------------------------------------------------------------------

# govulncheck is declared as a tool directive in go.mod, so the version is
# pinned by the module graph and CI runs the same one as a developer does.
# Analysis is call-graph based: a vulnerability in a dependency is only
# reported when a path to the affected symbol actually exists.
govulncheck: ## Scan Go dependencies for known vulnerabilities
	@if $(HAVE_GO_PKGS); then $(GO) tool govulncheck ./...; else echo "$(NO_PKGS_MSG) govulncheck"; fi

check: fmt-check vet lint test govulncheck ## Everything CI runs

##@ Development

# -------------------------------------------------------------------------
# CODE GENERATION
# -------------------------------------------------------------------------

generate: $(MOCKGEN) ## Generate interface mocks and the agent protocol bindings
	@if $(HAVE_GO_PKGS); then $(GO) generate ./...; else echo "$(NO_PKGS_MSG) generate"; fi
	$(GO) tool buf generate

# CI runs this to catch a mock that was not regenerated after its interface
# changed, which otherwise surfaces as a confusing compile failure later.
generate-check: generate ## Fail if generated code is out of date
	@if ! git diff --quiet; then \
		echo "error: generated code is out of date, run 'make generate'"; \
		git diff --stat; \
		exit 1; \
	fi

##@ Website

# -------------------------------------------------------------------------
# WEBSITE
# -------------------------------------------------------------------------

web-serve: ## Serve the project site locally, rebuilding on change
	cd web && hugo serve

web-build: ## Build the project site into web/public
	cd web && hugo --minify

web-docker: ## Build the site image for the local architecture
	docker build --pull --build-arg VERSION=$(WEB_TAG) -f web/Dockerfile -t $(WEB_IMAGE):$(WEB_TAG) .

web-push: builder ## Build and push the multi-arch site image
	docker buildx build --pull --platform $(PLATFORMS) --build-arg VERSION=$(WEB_TAG) -f web/Dockerfile \
		-t $(WEB_IMAGE):$(WEB_TAG) -t $(WEB_IMAGE):latest --output type=image,push=true .

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
	rm -f coverage.out integration-coverage.out vagabond
	rm -rf bin

.PHONY: help build builder docker scan push fmt fmt-check vet lint test test-fast integration-test coverage integration-coverage containerd-test govulncheck check generate generate-check web-serve web-build web-docker web-push tools clean
