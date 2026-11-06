FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/BillyGrande/Babis" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
COPY bin/babis-operator-linux-${TARGETARCH} /babis-operator
USER 65532:65532
ENTRYPOINT ["/babis-operator"]
