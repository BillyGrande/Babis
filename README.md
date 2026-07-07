# Babis

> Named after no god, no titan, no hero. **Babis is the guy who actually
> shows up and fixes it.**

Day-0 installation and fleet watch for **edge clusters running their own
ArgoCD** — online or air-gapped — with **outbound-only** connectivity from
the edges.

> **Status: pre-alpha.** APIs, CRDs, and the wire format will change
> without notice until v0.1. See the [roadmap](docs/roadmap.md).

## Why "Babis"?

Cloud-native infrastructure is steered by a Greek pantheon: Kubernetes the
helmsman, Argo the hero-ship, Istio the sail, Prometheus the titan. Babis
(Μπάμπης — the folksy Greek diminutive of *Charalampos*) is not in the
pantheon. Babis is the uncle with the toolbox. You call him when something
at the far end of nowhere needs installing, and afterwards he keeps an eye
on it and tells you if it breaks. That is the entire product.

## The problem

Most multi-cluster GitOps tooling assumes the hub can reach every
cluster's API server. Edge fleets are usually the opposite: hundreds of
small clusters behind NAT, flaky WAN links, sometimes no internet at all.
Two questions remain unanswered in that topology:

1. **How does the hundredth edge get ArgoCD the same way the first one
   did?** — including edges where "install" must happen from a local OCI
   registry instead of the internet.
2. **What state is the fleet in right now?** — without ever connecting
   *into* an edge.

Babis answers exactly these two, and deliberately nothing else
([positioning](docs/positioning.md)).

## How it works

- **`babis`** — a single-binary CLI that runs *on* the edge: install
  ArgoCD (`bootstrap`), apply the ArgoCD Applications that drive
  everything else (`apply`), inspect (`status`), and push a compact
  heartbeat to the central operator (`report`). Run `report` from cron —
  there is no resident agent.
- **operator** — runs in one central cluster. Reports materialize as
  `Edge` custom resources: sync/health per application, air-gap store
  version, local registry health, and a `Reachable` condition that flips
  when heartbeats stop. Your fleet dashboard is `kubectl get edges`.

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
BABIS_INGEST_TOKEN=change-me go run ./cmd/operator
```

On an edge (any cluster with `kubectl`/`helm` on PATH):

```bash
# day 0 — online edge
babis bootstrap --chart-version 7.7.0
babis apply ./apps --values edge-values.yaml

# day 0 — air-gapped edge (chart from a local OCI registry, e.g. Hauler or Zarf)
babis bootstrap --chart oci://localhost:5000/charts/argo-cd --plain-http

# heartbeat — add to cron / a systemd timer
BABIS_TOKEN=change-me babis report \
  --hub https://fleet.example.com --edge-name shop-athens-042 \
  --label region=gr-north --registry-url http://localhost:5000
```

## What Babis is not

Babis distributes no manifests between clusters, opens no connections into
edges, and runs no daemon on them. It is a complement to — never a
replacement for — [argocd-agent](https://github.com/argoproj-labs/argocd-agent)
(hub-spoke ArgoCD), [Zarf](https://zarf.dev)/[Hauler](https://hauler.dev)
(air-gap packaging and transport), and Fleet/Sveltos/OCM (manifest
distribution). The boundary is written down in
[docs/positioning.md](docs/positioning.md) and defended with litmus tests.

## Documentation

- [docs/architecture.md](docs/architecture.md) — full design and rationale
- [docs/positioning.md](docs/positioning.md) — scope, non-goals, ecosystem
- [docs/roadmap.md](docs/roadmap.md) — where this is going
- [CLAUDE.md](CLAUDE.md) — contributor conventions

## Development

```bash
make check   # fmt + vet + staticcheck + test
make build   # bin/babis, bin/babis-operator
```

## License

Apache-2.0
