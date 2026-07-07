# edge-deployer

> **Status: pre-alpha.** APIs, CRDs, and the wire format will change without
> notice until v0.1.

Day-0 installation and fleet observability for **edge clusters running
their own ArgoCD** — online or air-gapped — with **outbound-only**
connectivity from the edges.

Most multi-cluster GitOps tooling assumes the hub can reach every cluster's
API server. Edge fleets are usually the opposite: hundreds of small clusters
behind NAT, flaky WAN links, sometimes no internet at all. edge-deployer
embraces that topology:

- **`edgedeploy`** — a single-binary CLI that runs *on* the edge: install
  ArgoCD (from the internet or a local air-gap OCI registry), apply the
  ArgoCD Applications that drive everything else, and push a compact status
  report to the central operator. Run `report` from cron — there is no
  resident agent.
- **operator** — runs in one central cluster. Reports materialize as `Edge`
  custom resources: sync/health per application, air-gap store version,
  local registry health, and a `Reachable` condition that flips when
  heartbeats stop. Your fleet dashboard is `kubectl get edges`.

```
$ kubectl get edges
NAME             DISPLAY      APPS   SYNCED   HEALTHY   REACHABLE   LAST REPORT
shop-athens-042  Athens 42    12     12       12        True        2m
plant-north-007  North 7      12     11       10        True        1m
kiosk-crete-113               12     12       12        False       4h
```

## Quickstart

Central cluster:

```bash
kubectl apply -f config/crd/
kubectl create namespace edge-fleet
EDGEDEPLOY_INGEST_TOKEN=change-me go run ./cmd/operator
```

On an edge (any cluster with `kubectl`/`helm` on PATH):

```bash
# day 0 — online edge
edgedeploy bootstrap --chart-version 7.7.0
edgedeploy apply ./apps --values edge-values.yaml

# day 0 — air-gapped edge (chart from a local OCI registry, e.g. hauler)
edgedeploy bootstrap --chart oci://localhost:5000/charts/argo-cd --plain-http

# heartbeat — add to cron / a systemd timer
EDGEDEPLOY_TOKEN=change-me edgedeploy report \
  --hub https://fleet.example.com --edge-name shop-athens-042 \
  --label region=gr-north --registry-url http://localhost:5000
```

## Relation to argocd-agent, Fleet, Sveltos, ...

edge-deployer does **not** distribute manifests between clusters and does
not compete with [argocd-agent](https://github.com/argoproj-labs/argocd-agent),
Rancher Fleet, or Sveltos. It targets fleets where each edge runs a fully
autonomous ArgoCD (or is air-gapped enough that day-0 must happen from a
local artifact store such as [Hauler](https://hauler.dev)) and answers the
question those setups leave open: *what state is my fleet in right now?*

## Development

```bash
make check   # fmt + vet + staticcheck + test
make build   # bin/edgedeploy, bin/operator
```

See [docs/architecture.md](docs/architecture.md) for the full design and
[docs/positioning.md](docs/positioning.md) for scope; [CLAUDE.md](CLAUDE.md)
has the contributor conventions.

## License

Apache-2.0
