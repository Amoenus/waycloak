ARG UPSTREAM_IMAGE
FROM ${UPSTREAM_IMAGE}

ARG TARGETARCH
ARG COREDNS_UPSTREAM_COMMIT
ARG COREDNS_X_CRYPTO_VERSION
ARG WAYCLOAK_RELEASE
ARG WAYCLOAK_SOURCE_COMMIT

COPY --chown=65532:65532 --chmod=0755 coredns-${TARGETARCH} /coredns

LABEL org.opencontainers.image.source="https://github.com/Amoenus/waycloak" \
      org.opencontainers.image.version="${WAYCLOAK_RELEASE}" \
      org.opencontainers.image.revision="${WAYCLOAK_SOURCE_COMMIT}" \
      io.waycloak.coredns.upstream-commit="${COREDNS_UPSTREAM_COMMIT}" \
      io.waycloak.coredns.x-crypto-version="${COREDNS_X_CRYPTO_VERSION}"
