# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

ARG UPSTREAM_IMAGE
FROM ${UPSTREAM_IMAGE}

ARG UPSTREAM_IMAGE
ARG GLUETUN_UPSTREAM_COMMIT
ARG TARGETARCH
ARG WAYCLOAK_RELEASE
ARG WAYCLOAK_SOURCE_COMMIT

COPY --chmod=0755 bin/gluetun-entrypoint-linux-${TARGETARCH} /gluetun-entrypoint
COPY LICENSE /licenses/gluetun/LICENSE
COPY gluetun-dependency.patch /licenses/gluetun/gluetun-dependency.patch

LABEL org.opencontainers.image.description="Exact upstream Gluetun with release-gating dependency patches for Waycloak" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.revision="${WAYCLOAK_SOURCE_COMMIT}" \
      org.opencontainers.image.source="https://github.com/Amoenus/waycloak" \
      org.opencontainers.image.title="Waycloak Gluetun candidate" \
      org.opencontainers.image.version="${WAYCLOAK_RELEASE}" \
      io.waycloak.gluetun.upstream-commit="${GLUETUN_UPSTREAM_COMMIT}" \
      io.waycloak.gluetun.upstream-image="${UPSTREAM_IMAGE}"
