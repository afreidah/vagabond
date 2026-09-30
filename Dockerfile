# -------------------------------------------------------------------------------
# Vagabond - Server and Agent
#
# Author: Alex Freidah
#
# One image for both halves of the one binary: run it as `vagabond server ...`
# or `vagabond agent ...`. It runs as a non-root user; an agent, which creates
# cgroups and drives the host's containerd, is deployed as root and
# privileged. No health check: the server's route means nothing to an agent,
# so the deployment defines its own.
# -------------------------------------------------------------------------------

FROM --platform=$BUILDPLATFORM golang:1.27.0-alpine AS builder

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
# Runtime Image
# -------------------------------------------------------------------------

FROM alpine:3.21

ARG VERSION=dev

LABEL org.opencontainers.image.title="vagabond" \
      org.opencontainers.image.description="Compute broker for serverless jobs across public clouds and your own nodes" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/afreidah/vagabond"

# CA certificates for the cloud provider APIs
RUN apk upgrade --no-cache && \
    apk add --no-cache ca-certificates && \
    adduser -D -u 10001 appuser

COPY --from=builder /build/vagabond /usr/local/bin/

USER appuser

EXPOSE 4747 4748

ENTRYPOINT ["vagabond"]
