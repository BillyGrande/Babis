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

## Edges without install rights (ArgoCD notifications)

Use this when you cannot run a CronJob next to ArgoCD, but you can edit the
two objects of the ArgoCD notifications controller. The controller sends one
HTTP request per Application event to the hub. No `babis` binary runs on the
edge.

**You need:**

- ArgoCD with the notifications controller (it is part of the standard
  ArgoCD install) in namespace `argocd`.
- A hub URL that the notifications controller can reach (the operator
  ingest Service, port 8090, or an https URL, see below).
- The shared ingest token, and a unique edge name.

The files are in `deploy/edge-notifications/`. They add keys that start with
`babis`, and the key `subscriptions`, to `argocd-notifications-cm`. They add
the key `babis-token` to `argocd-notifications-secret`. In both patch files,
replace the placeholders first (kustomize cannot set a value inside a block
scalar):

| Placeholder | Where | Value |
|---|---|---|
| `EDGE_NAME` | `argocd-notifications-cm.yaml` | the unique edge name |
| `HUB_URL` | `argocd-notifications-cm.yaml` | for example `https://babis.example.com` |
| `TOKEN` | `argocd-notifications-secret.yaml` | the shared ingest token |

```sh
cp -r deploy/edge-notifications /tmp/edge-notifications && cd /tmp/edge-notifications
sed -i -e 's|EDGE_NAME|shop-athens-042|' -e 's|HUB_URL|https://babis.example.com|' argocd-notifications-cm.yaml
sed -i -e 's|TOKEN|<same token>|' argocd-notifications-secret.yaml
```

**Warning: `subscriptions`.** `subscriptions` is one key for the whole
controller. If your `argocd-notifications-cm` already has a `subscriptions`
value, this patch replaces it. Before you apply, read your value
(`kubectl -n argocd get cm argocd-notifications-cm -o jsonpath='{.data.subscriptions}'`),
and add the Babis item to your own list by hand in
`argocd-notifications-cm.yaml`:

```yaml
subscriptions: |
  - recipients: [your-recipient]     # your existing items stay
    triggers: [your-trigger]
  - recipients: [babis]
    triggers: [babis-state, babis-deleted]
```

**Apply, way A: a component.** In the kustomization that builds your ArgoCD
install, add the edited directory as a component:

```yaml
components:
  - edge-notifications
```

**Apply, way B: `kubectl patch`.** Each file is a valid merge patch:

```sh
kubectl -n argocd patch configmap argocd-notifications-cm --type merge --patch-file argocd-notifications-cm.yaml
kubectl -n argocd patch secret argocd-notifications-secret --type merge --patch-file argocd-notifications-secret.yaml
```

Then restart the notifications controller (the e2e test does the same):
`kubectl -n argocd rollout restart deploy/argocd-notifications-controller`.

**Check:**

```sh
kubectl get edges -o wide          # SOURCE shows argocd-notifications
kubectl -n argocd logs deploy/argocd-notifications-controller
```

**Limits:**

- The hub gets no ArgoCD version and no component status. `ArgoCDReady` is
  `Unknown` with reason `NotReported`.
- `Reachable` needs at least one Application. Each Application sends at
  least once every 10 minutes, so an edge without Applications is not
  reachable for the hub.
- A deleted Application leaves the Edge status at once only when it has the
  finalizer `resources-finalizer.argocd.argoproj.io`. Without the finalizer,
  the controller sends no `deleted` event, and the hub drops the Application
  after 45 minutes without events.
- Check-ins from the CronJob (`deploy/edge`) replace this status. Use one
  way for each edge name.

**Cost:** each send writes the annotation `notified.notifications.argoproj.io`
on the Application: about 6 extra writes per Application per hour, and one
for each state change. Babis uses no CPU or memory on the edge.

**HTTPS with a private CA.** The notifications controller trusts a private CA
through ArgoCD's `argocd-tls-certs-cm`, not through a Babis setting. Add the
CA certificate as a key named after the host name of the hub URL:

```sh
kubectl -n argocd patch configmap argocd-tls-certs-cm --type merge \
  --patch "$(printf 'data:\n  babis.example.com: |\n'; sed 's/^/    /' ca.crt)"
```

The controller looks up the certificate by server name. Restart the
controller after the change. Do not use `insecureSkipVerify`.
