# Deploy

Images: `make images` builds `babis-edge:dev` and `babis-operator:dev`
(`make images-arm64` builds the arm64 edge image). Push them to your registry
and override the names with `kustomize edit set image` (see below).

## Central cluster

```sh
kubectl apply -k deploy/central          # namespaces, CRD, RBAC, operator, Service
kubectl -n babis-system create secret generic babis-ingest-token \
  --from-literal=token="$(openssl rand -hex 32)"
```

The operator starts after the Secret exists (the Pod waits for it).
Use the same token value on every edge.

## Each edge (needs ArgoCD in namespace `argocd`)

```sh
kubectl -n argocd create secret generic babis-token --from-literal=token=<same token>
cd deploy/edge
kustomize edit set image babis-edge=registry.example.com/babis-edge:v0.1.0
# set hub and edgeName in configmap.yaml, then:
kubectl apply -k .
```

`hub` is the URL of the operator ingest Service (port 8090). `edgeName` must be
unique in the fleet. The Secrets are not part of the kustomizations on purpose.
