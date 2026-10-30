# Babis

> Named after no god, no titan, no hero. **Babis is the guy who actually
> shows up and fixes it.**

Fleet watch for **edge clusters running their own ArgoCD** — online or
air-gapped — with **outbound-only** connectivity from the edges. Day-0
installation of ArgoCD is planned.

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

1. **What state is the fleet in right now?** — without ever connecting
   *into* an edge. (Available today.)
2. **How does the hundredth edge get ArgoCD the same way the first one
   did?** — including edges where "install" must happen from a local OCI
   registry instead of the internet. (Planned: P2.)

Babis answers exactly these two, and deliberately nothing else
([positioning](docs/positioning.md)).

## How it works

- **`babis checkin`** — a small static binary (Rust) that runs *on* the
  edge from a CronJob in the `argocd` namespace. It reads the local ArgoCD
  state (Applications, ArgoCD components, version) with a read-only Role,
  sends one request to the hub, and exits. There is no resident agent, and
  the edge never accepts a connection.
- **operator** — runs in one central cluster (Go). It turns check-ins into
  `Edge` custom resources (`fleet.babis.dev/v1alpha1`) with two
  conditions: `Reachable` (the edge checked in recently) and `ArgoCDReady`
  (all ArgoCD components are ready). Your fleet dashboard is
  `kubectl get edges`.

```
$ kubectl get edges -n babis-fleet
NAME             ARGOCD    READY   APPS   SYNCED   HEALTHY   REACHABLE   LAST CHECKIN
shop-athens-042  v3.5.6    True    12     12       11        True        2m
kiosk-crete-113  v3.3.18   False   12     12       12        True        4m
plant-north-007  v3.5.6    True    9      9        9         False       4h
```

`babis bootstrap` (install ArgoCD) is phase P2. Desired state sent from
central to the edge is phase P3. See the [roadmap](docs/roadmap.md).

## Try it on kind

One kind cluster plays both the central hub and one edge. You need
`docker`, `kind`, `kubectl`, and the build toolchain from
[CLAUDE.md](CLAUDE.md) (Go, rustup, zig, cargo-zigbuild). The commands run
from the repository root.

```bash
# 1. Build the two images (babis-edge:dev and babis-operator:dev).
make images

# 2. Create the cluster and load the images into it.
kind create cluster --name babis-quickstart --wait 120s
kind load docker-image babis-edge:dev babis-operator:dev --name babis-quickstart

# 3. Install ArgoCD (the "edge" part).
kubectl create namespace argocd
kubectl apply -n argocd --server-side --force-conflicts \
  -f https://raw.githubusercontent.com/argoproj/argo-cd/v3.5.6/manifests/install.yaml
kubectl -n argocd rollout status deploy --timeout=300s
kubectl -n argocd rollout status statefulset --timeout=300s

# 4. Create the namespaces and the shared token (same value on both sides).
kubectl apply -f deploy/central/namespaces.yaml
TOKEN="$(openssl rand -hex 32)"
kubectl -n babis-system create secret generic babis-ingest-token --from-literal=token="$TOKEN"
kubectl -n argocd create secret generic babis-token --from-literal=token="$TOKEN"

# 5. Install the operator (central).
kubectl apply -k deploy/central
kubectl -n babis-system rollout status deploy/babis-operator --timeout=180s

# 6. Install the check-in CronJob (edge) and point it at the operator.
kubectl apply -k deploy/edge
kubectl -n argocd patch configmap babis-edge --type=merge \
  -p '{"data":{"hub":"http://babis-operator.babis-system.svc:8090","edgeName":"quickstart-edge"}}'

# 7. Do not wait for the next 5-minute tick: run one check-in now.
#    (The CronJob adds up to 60 s of random delay, so wait a moment.)
kubectl -n argocd create job --from=cronjob/babis-checkin checkin-now

# 8. Look at the fleet. ArgoCDReady can take a few minutes on a new cluster;
#    repeat the command until READY and REACHABLE show True.
kubectl get edges -n babis-fleet
```

Clean up with `kind delete cluster --name babis-quickstart`.

For a real deployment (your own registry, `kustomize edit set image`, one
token per fleet) see [deploy/README.md](deploy/README.md). In production,
put your own TLS ingress in front of the operator port `8090`; Babis does
not terminate TLS.

## What Babis is not

Babis distributes no manifests between clusters, opens no connections into
edges, and runs no daemon on them. On an edge it only reads ArgoCD state
and never changes ArgoCD, Applications, or any other object. It is a
complement to — never a replacement for —
[argocd-agent](https://github.com/argoproj-labs/argocd-agent)
(hub-spoke ArgoCD), [Zarf](https://zarf.dev)/[Hauler](https://hauler.dev)
(air-gap packaging and transport), and Fleet/Sveltos/OCM (manifest
distribution). The boundary is written down in
[docs/positioning.md](docs/positioning.md) and defended with litmus tests.

## Documentation

- [docs/architecture.md](docs/architecture.md) — how it works
- [docs/positioning.md](docs/positioning.md) — scope, non-goals, ecosystem
- [docs/roadmap.md](docs/roadmap.md) — phases P1 to P4 and what is done
- [deploy/README.md](deploy/README.md) — install on real clusters
- [changes/0002-redesign/spec.md](changes/0002-redesign/spec.md) — the design
- [wire/README.md](wire/README.md) — the check-in contract
- [CLAUDE.md](CLAUDE.md) — contributor conventions

## Development

```bash
make check        # central-check + edge-check (run before every commit)
make contract     # wire fixtures through Go and Rust only (fast)
make build        # bin/babis-operator and bin/babis (static musl)
make edge-budget  # edge binary <= 3 MB, peak RSS <= 5 MB at 300 apps
make images       # babis-edge:dev, babis-operator:dev
make e2e          # kind end-to-end, oldest and newest ArgoCD (~12 min)
make load         # 1000 simulated edges against the operator
```

Layout: `edge/` (Rust CLI), `central/` (Go operator), `wire/` (JSON Schemas
and golden fixtures), `deploy/` (kustomize manifests), `images/`
(Dockerfiles), `test/` (e2e, load, budget), `docs/`, `changes/`
(intent, spec, plan per change).

## License

Apache-2.0
