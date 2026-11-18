# -------------------------------------------------------------------------------
# vagabond-firecracker
#
# Author: Alex Freidah
#
# The daemon that runs the agent's workloads in Firecracker microVMs, with all
# it boots them from: vagabond-init packed as the initramfs, Firecracker and its
# jailer, and the guest kernel. amd64 only. It runs as root and privileged
# beside the agent, on the host's network, with /dev/kvm, the host's cgroup
# tree, and the state and jail directories from the host; the agent reaches it
# through its socket. Firecracker, the jailer and the kernel come pinned and
# checksummed from make, through the kvm build context.
# -------------------------------------------------------------------------------

FROM rust:1.100.0-alpine3.21 AS builder

# cpio packs the initramfs.
RUN apk add --no-cache cpio

# The protocols sit where build.rs reads them, two levels above the crate.
WORKDIR /build
COPY internal/agentrpc/*.proto internal/agentrpc/
COPY rust/vagabond-firecracker/ rust/vagabond-firecracker/

# Static on Alpine, whose target is musl, so the init needs nothing beside it.
WORKDIR /build/rust/vagabond-firecracker
RUN cargo build --release --locked --bins

# The initramfs is the init alone, as /init and owned by root.
RUN mkdir /initramfs && cp target/release/vagabond-init /initramfs/init && \
    cd /initramfs && echo init | cpio --quiet -o -H newc -R 0:0 | gzip > /initramfs.cpio.gz

# -------------------------------------------------------------------------
# Runtime Image
# -------------------------------------------------------------------------

FROM alpine:3.21

ARG VERSION=dev

LABEL org.opencontainers.image.title="vagabond-firecracker" \
      org.opencontainers.image.description="Runs the vagabond agent's workloads in Firecracker microVMs" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/afreidah/vagabond"

# ip makes each microVM's tap, nft its NAT, and iptables its way through
# Docker's FORWARD chain.
RUN apk upgrade --no-cache && \
    apk add --no-cache iproute2 iptables nftables

COPY --from=builder /build/rust/vagabond-firecracker/target/release/vagabond-firecracker /usr/local/bin/
COPY --from=builder /initramfs.cpio.gz /usr/share/vagabond-firecracker/

# Where the daemon looks for Firecracker and the jailer by default.
COPY --from=kvm firecracker jailer /usr/bin/
COPY --from=kvm vmlinux /usr/share/vagabond-firecracker/

ENTRYPOINT ["vagabond-firecracker", \
    "--kernel", "/usr/share/vagabond-firecracker/vmlinux", \
    "--initramfs", "/usr/share/vagabond-firecracker/initramfs.cpio.gz"]
