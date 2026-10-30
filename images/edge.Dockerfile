# Build context: the directory that holds the prebuilt static binary.
# Use `make images` (amd64) or `make images-arm64`.
FROM scratch
ARG BINARY=bin/babis
COPY ${BINARY} /babis
USER 65532:65532
ENTRYPOINT ["/babis"]
