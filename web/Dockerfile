# -------------------------------------------------------------------------------
# Vagabond - Project Site
#
# Author: Alex Freidah
#
# Builds the Hugo site from web/ and docs/ and serves it with nginx. The build
# image carries Go and Git, which Hugo needs to fetch the Hextra theme module
# pinned in web/go.mod.
# -------------------------------------------------------------------------------

FROM --platform=$BUILDPLATFORM hugomods/hugo:go-git-0.157.0 AS builder

WORKDIR /src/web

# The image pins GOTOOLCHAIN=local to its own Go; auto fetches the version
# web/go.mod names, which Hugo's module download runs under
ENV GOTOOLCHAIN=auto

# Copy the site and the docs it mounts
COPY web/ /src/web/
COPY docs/ /src/docs/

# Fetch the theme module and build the site
RUN hugo --minify

# -------------------------------------------------------------------------
# Runtime Image
# -------------------------------------------------------------------------

FROM nginx:alpine

ARG VERSION=dev
# Changes per build (CI passes the commit SHA), so the upgrade layer is never
# reused from cache
ARG APK_CACHEBUST=0

LABEL org.opencontainers.image.title="vagabond-web" \
      org.opencontainers.image.description="Vagabond project site and documentation" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/afreidah/vagabond"

RUN APK_CACHEBUST="$APK_CACHEBUST" apk upgrade --no-cache

COPY --from=builder /src/web/public /usr/share/nginx/html
COPY web/nginx-cache.conf /etc/nginx/conf.d/nginx-cache.conf

EXPOSE 80

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://localhost/ >/dev/null || exit 1
