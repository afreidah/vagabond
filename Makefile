# -------------------------------------------------------------------------------
# Vagabond - Build, Test, and Quality Gates
#
# Author: Alex Freidah
#
# Common development tasks for the Vagabond control plane, CLI and
# vagabond-firecracker. `make check` is the gate a change must pass before it is
# pushed, and it is what CI runs. Every target is safe to run against a tree
# with no Go packages.
# -------------------------------------------------------------------------------

GO             ?= go
GOBIN          ?= $(shell $(GO) env GOPATH)/bin

# Prefer a golangci-lint already on PATH over installing another copy. CI
# installs it from a release binary and then runs these same targets, so the
# linter a pull request is judged by is the one a developer ran locally.
GOLANGCI_LINT  := $(shell command -v golangci-lint 2>/dev/null || echo $(GOBIN)/golangci-lint)
MOCKGEN        := $(shell command -v mockgen 2>/dev/null || echo $(GOBIN)/mockgen)

# Pinned so that a lint failure is a code change rather than a tool upgrade.
GOLANGCI_VERSION ?= v2.14.0
MOCKGEN_VERSION  ?= v0.6.0
CARGO_AUDIT_VERSION    ?= 0.23.0
CARGO_LLVM_COV_VERSION ?= 0.9.1

# The Rust crate. Its targets run in it, where rust-toolchain.toml picks the
# compiler, rustfmt and clippy.
FC_CRATE       := rust/vagabond-firecracker

# What a microVM boots, and the root drive the microVM tests give it, pinned
# with checksums and fetched once into the crate's target directory. The kernel
# is Firecracker's own test kernel, which has the IP autoconfig, vsock and
# virtio disk the guest init needs built in. KVM_IMAGE is what the
# vagabond-firecracker image takes from here.
KVM_DIR          := $(FC_CRATE)/target/kvm
FC_VERSION       := v1.18.0
FC_SHA256        := 1ac8eec866dd5eea14efecf6323e22b23eea24054545338a5b34bc5bca6623cd
FC_KERNEL        := firecracker-ci/v1.15/x86_64/vmlinux-6.1.155
FC_KERNEL_SHA256 := e20e46d0c36c55c0d1014eb20576171b3f3d922260d9f792017aeff53af3d4f2
ALPINE_ROOTFS    := alpine-minirootfs-3.20.10-x86_64.tar.gz
ALPINE_SHA256    := 0d44a414245f043ae56872a23762e6c9bee585da2cbe4aa81cbd0331eaf53223
KVM_IMAGE        := $(addprefix $(KVM_DIR)/,firecracker jailer vmlinux)

# The CNI plugins the agent attaches containers with, pinned with checksums and
# fetched once per architecture into CNI_DIR. The image takes each
# architecture's archive, and the executor tests the plugins for this machine.
CNI_DIR          := bin/cni
CNI_VERSION      := v1.10.0
CNI_SHA256_amd64 := ce8603d6a1bd448efba5bd6dc44f38dce349f821aa57e1bb354b52628ebf1853
CNI_SHA256_arm64 := 4f25d7f136be368746185fba6557d799cf3e1890f7556d450744ea20d300d68c
CNI_PLUGINS      := ptp host-local loopback
CNI_IMAGE        := $(CNI_DIR)/cni-plugins-amd64.tgz $(CNI_DIR)/cni-plugins-arm64.tgz
CNI_BIN          := $(CNI_DIR)/$(shell go env GOARCH)

# Injected into internal/version at link time, so a built binary reports what it
# actually is rather than whatever string was last committed. Read from
# .version, which CI requires a pull request to bump when it changes anything
# an image is built from; dev when the file is missing.
VERSION        ?= $(shell cat .version 2>/dev/null || echo dev)
COMMIT         ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
VERSION_PKG    := github.com/afreidah/vagabond/internal/version
GO_LDFLAGS     := -s -w \
	-X $(VERSION_PKG).Version=$(VERSION) \
	-X $(VERSION_PKG).Commit=$(COMMIT)

# Where install puts the binary. The default needs sudo; set
# BINDIR=$(HOME)/.local/bin to install without it.
PREFIX         ?= /usr/local
BINDIR         ?= $(PREFIX)/bin

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

install: build ## Install the binary to BINDIR (default /usr/local/bin; needs sudo, or set BINDIR=$(HOME)/.local/bin)
	install -d $(BINDIR)
	install -m 0755 vagabond $(BINDIR)/vagabond
	@echo "installed vagabond $(VERSION) -> $(BINDIR)/vagabond"

uninstall: ## Remove the installed binary from BINDIR
	rm -f $(BINDIR)/vagabond
	@echo "removed $(BINDIR)/vagabond"

##@ Images

# -------------------------------------------------------------------------
# IMAGES
# -------------------------------------------------------------------------

builder: ## Ensure the buildx builder exists
	@docker buildx inspect vagabond-builder >/dev/null 2>&1 || \
		docker buildx create --name vagabond-builder --driver-opt network=host --use
	@docker buildx inspect --bootstrap

# image builds $(1) from the Dockerfile $(2), with the flags $(3), from the
# repository: for the local architecture into Docker, or for the platforms $(4)
# pushed, tagged with latest as well as the version, so a deployment can pin
# one or follow the other.
comma := ,
image = docker buildx build --pull -f $(2) $(3) \
	--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(1):$(VERSION) \
	$(if $(4),--platform $(4) -t $(1):latest --output type=image$(comma)push=true,--load) .

# Handed Firecracker, the jailer and the kernel as fetched and checked here,
# which the amd64 image carries for the agent's -firecracker, and the CNI
# plugin archives, which every image carries for the agent's containers.
docker: $(KVM_IMAGE) $(CNI_IMAGE) ## Build the vagabond image for the local architecture
	$(call image,$(IMAGE),Dockerfile,--build-context kvm=$(KVM_DIR) --build-context cni-plugins=$(CNI_DIR))

scan: docker ## Scan the local vagabond image with Trivy
	trivy image --severity CRITICAL,HIGH $(IMAGE):$(VERSION)

push: builder $(KVM_IMAGE) $(CNI_IMAGE) ## Build and push the multi-arch vagabond image
	$(call image,$(IMAGE),Dockerfile,--build-context kvm=$(KVM_DIR) --build-context cni-plugins=$(CNI_DIR),$(PLATFORMS))

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

# rustfmt's default style, with no configuration.
rust-fmt: ## Apply rustfmt to the Rust crate
	cd $(FC_CRATE) && cargo fmt

rust-fmt-check: ## Verify the Rust crate's formatting without modifying files
	cd $(FC_CRATE) && cargo fmt --check

rust-lint: ## Run clippy on the Rust crate and its tests, failing on any warning
	cd $(FC_CRATE) && cargo clippy --all-targets -- -D warnings

# -------------------------------------------------------------------------
# TESTING
# -------------------------------------------------------------------------

test: ## Run Go tests with the race detector
	@if $(HAVE_GO_PKGS); then $(GO) test -race ./...; else echo "$(NO_PKGS_MSG) test"; fi

test-fast: ## Run Go tests without the race detector for quick iteration
	@if $(HAVE_GO_PKGS); then $(GO) test ./...; else echo "$(NO_PKGS_MSG) test"; fi

# The microVM tests are ignored here; firecracker-test runs them.
rust-test: ## Run the Rust crate's tests
	cd $(FC_CRATE) && cargo test

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

# cargo-llvm-cov builds the crate instrumented with the toolchain's llvm-tools
# and writes LCOV, the format SonarCloud reads for Rust.
rust-coverage: ## Write rust-coverage.lcov from the Rust crate's tests
	cd $(FC_CRATE) && cargo llvm-cov --lcov --output-path $(CURDIR)/rust-coverage.lcov

# -------------------------------------------------------------------------
# FIRECRACKER TESTS
# -------------------------------------------------------------------------

# fetch downloads $(1) to the target and fails unless its sha256 is $(2). It
# goes through a partial file, so an interrupted download never counts as done.
fetch = mkdir -p $(@D) && curl -fsSL -o $@.part $(1) && \
	echo "$(2)  $@.part" | sha256sum --check --quiet && mv $@.part $@

$(KVM_DIR)/firecracker.tgz:
	$(call fetch,https://github.com/firecracker-microvm/firecracker/releases/download/$(FC_VERSION)/firecracker-$(FC_VERSION)-x86_64.tgz,$(FC_SHA256))

# The release holds each binary under its versioned name; the tests take the
# plain one. -m dates it now rather than at the release, so it is newer than
# the archive and make extracts it once.
$(KVM_DIR)/firecracker $(KVM_DIR)/jailer: $(KVM_DIR)/firecracker.tgz
	tar -xzmf $< -C $(@D) --strip-components=1 release-$(FC_VERSION)-x86_64/$(@F)-$(FC_VERSION)-x86_64
	mv $(@D)/$(@F)-$(FC_VERSION)-x86_64 $@

$(KVM_DIR)/vmlinux:
	$(call fetch,https://s3.amazonaws.com/spec.ccfc.min/$(FC_KERNEL),$(FC_KERNEL_SHA256))

$(KVM_DIR)/$(ALPINE_ROOTFS):
	$(call fetch,https://dl-cdn.alpinelinux.org/alpine/v3.20/releases/x86_64/$(ALPINE_ROOTFS),$(ALPINE_SHA256))

# Each architecture's CNI plugin archive, checked against its own checksum.
$(CNI_DIR)/cni-plugins-%.tgz:
	$(call fetch,https://github.com/containernetworking/plugins/releases/download/$(CNI_VERSION)/cni-plugins-linux-$*-$(CNI_VERSION).tgz,$(CNI_SHA256_$*))

# The plugins the executor tests use, for this machine. -m dates them now, so
# they are newer than the archive and make extracts them once.
$(CNI_BIN): $(CNI_DIR)/cni-plugins-$(shell go env GOARCH).tgz
	mkdir -p $@ && tar -xzmf $< -C $@ $(addprefix ./,$(CNI_PLUGINS))

# The guest init, static so the initramfs needs nothing beside it. Always run,
# since cargo decides itself what needs rebuilding, and run in the crate, where
# rust-toolchain.toml picks the compiler.
firecracker-init:
	cd $(FC_CRATE) && cargo build --release --target x86_64-unknown-linux-musl --bin vagabond-init

# The initramfs is the init alone, as /init and owned by root. The init makes
# its own mountpoints, and the kernel's built-in initramfs has /dev/console.
$(KVM_DIR)/initramfs.cpio.gz: firecracker-init
	rm -rf $(KVM_DIR)/initramfs && mkdir -p $(KVM_DIR)/initramfs
	cp $(FC_CRATE)/target/x86_64-unknown-linux-musl/release/vagabond-init $(KVM_DIR)/initramfs/init
	cd $(KVM_DIR)/initramfs && echo init | cpio --quiet -o -H newc -R 0:0 | gzip > ../initramfs.cpio.gz

# The root drive each test copies for its microVM: Alpine as an ext4 image,
# written straight from the tarball, so its files are root's without sudo.
MKFS_EXT4 ?= /usr/sbin/mkfs.ext4

$(KVM_DIR)/rootfs.ext4: $(KVM_DIR)/$(ALPINE_ROOTFS)
	truncate -s 64M $@.part && $(MKFS_EXT4) -q -F -d $< $@.part && mv $@.part $@

# What a microVM boots, which vagabond-firecracker runs from, and with the root
# drive the microVM tests copy, what they need.
KVM_BOOT  := $(KVM_IMAGE) $(KVM_DIR)/initramfs.cpio.gz
KVM_FILES := $(KVM_BOOT) $(KVM_DIR)/rootfs.ext4

# finally runs $(1), then $(2) whether or not $(1) passed, and exits as $(1)
# did, so a test run as root still hands back what it wrote, or clears what it
# started, when it fails.
finally = $(1); status=$$?; $(2); exit $$status

# The tests launch Firecracker through the jailer, which needs root. cargo
# builds them as the user and runs each test binary through sudo as its
# runner, so the build cache stays the user's; env hands the binary where the
# files above are.
firecracker-test: $(KVM_FILES) ## Boot microVMs through vagabond-firecracker (requires root, /dev/kvm and a network uplink)
	cd $(FC_CRATE) && \
		CARGO_TARGET_X86_64_UNKNOWN_LINUX_GNU_RUNNER="sudo env VAGABOND_KVM_DIR=$(abspath $(KVM_DIR))" \
		cargo test --test kvm -- --ignored

# The same tests instrumented, the daemon they start included. sudo keeps
# LLVM_PROFILE_FILE, without which nothing is written, and the profiles written
# as root are handed back to the user so the next run can clear them.
firecracker-coverage: $(KVM_FILES) ## Write firecracker-coverage.lcov from the microVM tests (requires root, /dev/kvm and a network uplink)
	cd $(FC_CRATE) && $(call finally, \
		CARGO_TARGET_X86_64_UNKNOWN_LINUX_GNU_RUNNER="sudo --preserve-env=LLVM_PROFILE_FILE env VAGABOND_KVM_DIR=$(abspath $(KVM_DIR))" \
		cargo llvm-cov --lcov --output-path $(CURDIR)/firecracker-coverage.lcov --test kvm -- --ignored, \
		sudo chown -R "$$(id -u):$$(id -g)" target/llvm-cov-target)

# -------------------------------------------------------------------------
# EXECUTOR TESTS
# -------------------------------------------------------------------------

# The agent's executors are tested against a containerd and a
# vagabond-firecracker of their own, apart from anything the host runs, which
# executor-env starts as root and executor-env-down clears away. The tests find
# them by the sockets in EXECUTOR_SOCKETS, and skip without them, and attach
# containers with the CNI plugins it names.
EXECUTOR_ENV     = sudo env KVM_DIR=$(abspath $(KVM_DIR)) DAEMON=$(abspath $(FC_CRATE))/target/debug/vagabond-firecracker scripts/executor-env.sh
EXECUTOR_SOCKETS = VAGABOND_CONTAINERD=/run/vagabond-test/containerd.sock VAGABOND_FIRECRACKER=/run/vagabond-test/firecracker.sock VAGABOND_CNI=$(abspath $(CNI_BIN))

firecracker-daemon:
	cd $(FC_CRATE) && cargo build --bin vagabond-firecracker

executor-env: firecracker-daemon $(KVM_BOOT) $(CNI_BIN) ## Start the executor tests' containerd and vagabond-firecracker (requires root, /dev/kvm, containerd and dmsetup)
	$(EXECUTOR_ENV) up

executor-env-down: ## Stop the executor tests' containerd and vagabond-firecracker, and clear what they left
	$(EXECUTOR_ENV) down

# Both executors' tests are one binary, built as the user and run with sudo so
# the build cache stays the user's, and the environment is cleared after.
executor-test: executor-env ## Run the executor tests against their own containerd and vagabond-firecracker (requires root and /dev/kvm)
	$(GO) test -c -race -tags=containerd,firecracker -o bin/executor.test ./internal/agent/executor
	$(call finally, \
		sudo env $(EXECUTOR_SOCKETS) ./bin/executor.test -test.v -test.timeout 10m, \
		$(EXECUTOR_ENV) down)

executor-coverage: executor-env ## Write executor-coverage.out from the executor tests (requires root and /dev/kvm)
	$(GO) test -c $(COVER_FLAGS) -tags=containerd,firecracker -o bin/executor.test ./internal/agent/executor
	$(call finally, \
		sudo env $(EXECUTOR_SOCKETS) ./bin/executor.test -test.v -test.timeout 10m -test.coverprofile=executor-coverage.out, \
		$(EXECUTOR_ENV) down; sudo chown "$$(id -u):$$(id -g)" executor-coverage.out)

# -------------------------------------------------------------------------
# SECURITY
# -------------------------------------------------------------------------

# govulncheck is declared as a tool directive in go.mod, so the version is
# pinned by the module graph and CI runs the same one as a developer does.
# Analysis is call-graph based: a vulnerability in a dependency is only
# reported when a path to the affected symbol actually exists.
govulncheck: ## Scan Go dependencies for known vulnerabilities
	@if $(HAVE_GO_PKGS); then $(GO) tool govulncheck ./...; else echo "$(NO_PKGS_MSG) govulncheck"; fi

# cargo-audit checks Cargo.lock against the RustSec advisory database, at the
# version tools installs.
rust-audit: ## Scan the Rust crate's dependencies for known vulnerabilities
	cd $(FC_CRATE) && cargo audit

check: fmt-check vet lint test govulncheck rust-fmt-check rust-lint rust-test rust-audit ## Everything CI runs

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
	$(call image,$(WEB_IMAGE),web/Dockerfile)

web-push: builder ## Build and push the multi-arch site image
	$(call image,$(WEB_IMAGE),web/Dockerfile,,$(PLATFORMS))

##@ Tools

# -------------------------------------------------------------------------
# TOOL INSTALLATION
# -------------------------------------------------------------------------

tools: ## Install build and lint dependencies into GOPATH/bin and ~/.cargo/bin
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	$(GO) install go.uber.org/mock/mockgen@$(MOCKGEN_VERSION)
	cargo install --locked cargo-audit@$(CARGO_AUDIT_VERSION)
	cargo install --locked cargo-llvm-cov@$(CARGO_LLVM_COV_VERSION)

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
	rm -f coverage.out integration-coverage.out executor-coverage.out vagabond rust-coverage.lcov firecracker-coverage.lcov
	rm -rf bin

.PHONY: help build install uninstall builder docker scan push fmt fmt-check vet lint rust-fmt rust-fmt-check rust-lint test test-fast rust-test rust-audit integration-test coverage integration-coverage rust-coverage firecracker-init firecracker-test firecracker-coverage firecracker-daemon executor-env executor-env-down executor-test executor-coverage govulncheck check generate generate-check web-serve web-build web-docker web-push tools clean
