FROM gcr.io/distroless/static-debian12:nonroot
COPY bin/babis-operator /babis-operator
USER 65532:65532
ENTRYPOINT ["/babis-operator"]
