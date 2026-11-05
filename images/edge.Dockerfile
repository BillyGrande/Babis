# Build context: the repo root, with prebuilt static binaries in bin/.
# Use `make images` (amd64) or `make images-arm64`. Release builds use
# `docker buildx build --platform linux/amd64,linux/arm64`.
FROM scratch
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/BillyGrande/Babis" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
COPY bin/babis-linux-${TARGETARCH} /babis
USER 65532:65532
ENTRYPOINT ["/babis"]
