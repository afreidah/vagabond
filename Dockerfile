# -------------------------------------------------------------------------------
# Vagabond - Server and Agent
#
# Author: Alex Freidah
#
# One image for both halves of the one binary: run it as `vagabond server ...`
# or `vagabond agent ...`. It runs as a non-root user; an agent, which creates
# cgroups and drives the host's containerd, is deployed as root and
# privileged. On amd64 it also carries what an agent's -firecracker runs:
# vagabond-firecracker, vagabond-init packed as the initramfs, and Firecracker,
# its jailer and the guest kernel, pinned and checksummed by make and handed in
# as the kvm build context. No health check: the server's route means nothing
# to an agent, so the deployment defines its own.
# -------------------------------------------------------------------------------

FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS builder

ARG VERSION=dev
ARG COMMIT=unknown
ARG TARGETOS
ARG TARGETARCH

WORKDIR /build

# Copy go module files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/ cmd/
COPY internal/ internal/

# Build binary (native cross-compilation, no QEMU needed)
RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build \
    -ldflags="-s -w -X github.com/afreidah/vagabond/internal/version.Version=${VERSION} -X github.com/afreidah/vagabond/internal/version.Commit=${COMMIT}" \
    -o vagabond ./cmd/vagabond

# -------------------------------------------------------------------------
# Firecracker
# -------------------------------------------------------------------------

# amd64, the one architecture Firecracker runs on here, built natively there.
FROM --platform=linux/amd64 rust:1.100.0-alpine3.21 AS firecracker-amd64

# cpio packs the initramfs.
RUN apk add --no-cache cpio

# The protocols sit where build.rs reads them, two levels above the crate.
WORKDIR /build
COPY internal/agentrpc/*.proto internal/agentrpc/
COPY rust/vagabond-firecracker/ rust/vagabond-firecracker/

# Static on Alpine, whose target is musl, so the init needs nothing beside it.
WORKDIR /build/rust/vagabond-firecracker
RUN cargo build --release --locked --bins

# Laid out under /out as the runtime image takes it. The initramfs is the init
# alone, as /init and owned by root.
RUN mkdir -p /out/usr/local/bin /out/usr/share/vagabond-firecracker /initramfs && \
    cp target/release/vagabond-firecracker /out/usr/local/bin/ && \
    cp target/release/vagabond-init /initramfs/init && \
    cd /initramfs && \
    echo init | cpio --quiet -o -H newc -R 0:0 | \
    gzip > /out/usr/share/vagabond-firecracker/initramfs.cpio.gz
COPY --from=kvm firecracker jailer /out/usr/bin/
COPY --from=kvm vmlinux /out/usr/share/vagabond-firecracker/

# Nothing on arm64 until Firecracker runs there.
FROM scratch AS firecracker-arm64
WORKDIR /out

FROM firecracker-${TARGETARCH} AS firecracker

# -------------------------------------------------------------------------
# Runtime Image
# -------------------------------------------------------------------------

FROM alpine:3.21

ARG VERSION=dev

LABEL org.opencontainers.image.title="vagabond" \
      org.opencontainers.image.description="Compute broker for serverless jobs across public clouds and your own nodes" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/afreidah/vagabond"

# CA certificates for the cloud provider APIs; ip, nft and iptables for each
# microVM's tap, NAT and way through Docker's FORWARD chain
RUN apk upgrade --no-cache && \
    apk add --no-cache ca-certificates iproute2 iptables nftables && \
    adduser -D -u 10001 appuser

COPY --from=builder /build/vagabond /usr/local/bin/
COPY --from=firecracker /out/ /

USER appuser

EXPOSE 4747 4748

ENTRYPOINT ["vagabond"]
