# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

ARG UPSTREAM_IMAGE
FROM ${UPSTREAM_IMAGE}

ARG UPSTREAM_IMAGE
ARG GLUETUN_UPSTREAM_COMMIT
ARG GLUETUN_SERVERS_COMMIT
ARG TARGETARCH
ARG WAYCLOAK_RELEASE
ARG WAYCLOAK_SOURCE_COMMIT

# The exact stable upstream image can lag security-fixed Alpine packages.
# Pin the narrow fixed set so rebuilds do not silently consume a later package.
RUN apk add --no-cache --upgrade \
      libcrypto3=3.5.8-r0 \
      libssl3=3.5.8-r0 \
      openssl=3.5.8-r0

COPY --chmod=0755 bin/gluetun-entrypoint-linux-${TARGETARCH} /gluetun-entrypoint
COPY --chmod=0444 control-auth.toml /etc/waycloak/gluetun-control-auth.toml
COPY LICENSE /licenses/gluetun/LICENSE
COPY gluetun-dependency.patch /licenses/gluetun/gluetun-dependency.patch
COPY gluetun-servers.LICENSE /licenses/gluetun-servers/LICENSE
COPY gluetun-servers.ref /licenses/gluetun-servers/gluetun-servers.ref
COPY gluetun-servers.SHA256SUMS /licenses/gluetun-servers/gluetun-servers.SHA256SUMS

LABEL org.opencontainers.image.description="Exact upstream Gluetun with release-gating dependency patches for Waycloak" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.revision="${WAYCLOAK_SOURCE_COMMIT}" \
      org.opencontainers.image.source="https://github.com/Amoenus/waycloak" \
      org.opencontainers.image.title="Waycloak Gluetun candidate" \
      org.opencontainers.image.version="${WAYCLOAK_RELEASE}" \
      io.waycloak.gluetun.upstream-commit="${GLUETUN_UPSTREAM_COMMIT}" \
      io.waycloak.gluetun.upstream-image="${UPSTREAM_IMAGE}" \
      io.waycloak.gluetun.servers-commit="${GLUETUN_SERVERS_COMMIT}"
