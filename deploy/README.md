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

## TLS

Babis does not terminate TLS. Put an ingress with TLS in front of the
operator Service (port 8090). Set `hub` in `configmap.yaml` to the https URL,
for example `https://babis.example.com`.

- **Public certificate** (for example Let's Encrypt): the edge needs nothing
  more. It trusts the built-in public roots.
- **Private CA**: the edge must trust your CA. On each edge, create a
  ConfigMap `babis-hub-ca` in `argocd` with the CA certificate under the key
  `ca.crt`. Then use the `hub-ca` component. It mounts the ConfigMap and adds
  `--hub-ca /etc/babis/hub-ca/ca.crt` to the CronJob.

```sh
kubectl -n argocd create configmap babis-hub-ca --from-file=ca.crt=./ca.crt
```

Overlay `kustomization.yaml` (next to a copy of `deploy/edge`, or with
`resources` pointing at it):

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../edge
components:
  - ../edge/hub-ca
```

With `--hub-ca`, the edge trusts only that CA. The public roots do not apply.
