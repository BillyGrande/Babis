# Babis

## What This Is

A hub for fleets of **autonomous ArgoCD edge clusters** (air-gapped included),
with no inbound connectivity to edges. Two deliverables: the **`babis` CLI**
(Rust) that runs one-shot on each edge — `checkin` reports ArgoCD state, later
`bootstrap` installs ArgoCD — and a central **operator** (Go) that turns
check-ins into `Edge` CRs with `Reachable` / `ArgoCDReady` conditions.
Complements — does not replace — [argocd-agent]. Read `docs/positioning.md`
(hard non-goals, litmus tests) before adding features. The current design is
`changes/0002-redesign/spec.md`; `docs/architecture.md` summarizes it.

[argocd-agent]: https://github.com/argoproj-labs/argocd-agent

## Layout

```
edge/                    # Rust crate, binary `babis` (toolchain pinned in rust-toolchain.toml)
central/                 # Go module github.com/BillyGrande/babis/central
  cmd/babis-operator/    # operator entry
wire/                    # check-in JSON Schemas + golden fixtures (testdata/)
deploy/{edge,central}/   # kustomize manifests (deploy/README.md: install steps)
images/                  # Dockerfiles (prebuilt static binaries copied in)
test/e2e/                # kind end-to-end, oldest + newest supported ArgoCD
test/budget/             # edge size / peak-RSS budget
docs/                    # architecture, positioning, roadmap
changes/                 # intent/spec/plan per change
```

Target layout inside `edge/` and `central/`: `changes/0002-redesign/plan.md`.
Generated files (`central/config/crd/`, `central/config/rbac/`, deepcopy) —
**never hand-edit**.

## Design Rules

- **ArgoCD-only, by design.** No abstraction over other GitOps engines (no Flux support, no generic layer). Prefer ArgoCD's own integration points — Application status, the cluster secret, notifications, the upstream install manifests/chart — over new Babis mechanisms.
- **Outbound-only edges.** Nothing may require central to reach an edge.
- **One-shot on the edge, long-lived only centrally.** `babis` runs from a CronJob/timer and exits; no resident agent. Reconciliation lives in the operator.
- **CRDs are the state store.** Fleet state is Edge status, not a database; history would be a separate consumer.
- **No Kubernetes client library on the edge.** The Rust CLI calls the API server with ureq + serde, streaming decode, paginated LIST. It shells out only to `helm` (P2+). The operator uses controller-runtime.
- **One wire contract.** Schemas and fixtures in `wire/` are the source of truth; Go and Rust tests both run against them. Released changes must be backward compatible (old edges keep reporting to new operators).
- **No package-level mutable state.** Build commands/servers via constructors; pass contexts into every exec and HTTP call.
- **Tests are table-driven** and need no cluster: pure logic plus handler tests on controller-runtime's fake client.
- **Boring on purpose.** Central: controller-runtime and what it drags in. Edge: ureq, serde, serde_json, lexopt. A new dependency needs a reason in the commit message.

## Dev Workflow

```bash
make check             # central-check + edge-check — run before every commit
make central-check     # gofmt, vet, staticcheck, go test (in central/)
make edge-check        # cargo fmt --check, clippy -D warnings, cargo test (in edge/)
make fmt               # gofmt -w + cargo fmt
make contract          # wire fixtures through Go and Rust only (fast)
make build             # bin/babis-operator and bin/babis (static musl, cargo zigbuild)
make build-edge-arm64  # bin/babis-linux-arm64
make generate manifests  # controller-gen: deepcopy, CRD, RBAC (central/config/)
make edge-budget       # edge binary <= 3 MB, peak RSS <= 5 MB at 300 apps
make images            # babis-edge:dev, babis-operator:dev (images-arm64 for the edge)
make e2e               # kind end-to-end, ~12 min, needs docker + internet
```

Needs rustup, zig and cargo-zigbuild locally (Arch: `pacman -S rustup zig
cargo-zigbuild`), plus docker, kind and kubectl for `images` and `e2e`.

## Verification (before reporting done)

- Run `make check`. It must pass.
- If `central/api/` or an RBAC marker changed: run `make generate manifests` and include the generated files in the same commit. CI fails on drift.
- If the wire format changed: update the schema and fixtures in `wire/` first; both the Go and the Rust contract tests must pass; the change must be backward compatible.
- New logic gets a table-driven test that needs no cluster.
- Edge changes: `make edge-budget` passes.
- Changes to `deploy/`, ingest, the reconciler or the edge's Kubernetes calls: `make e2e` passes.

## Change workflow

This repo follows the [AI-native SDLC playbook]: each stage commits an
artifact that the next stage reads, and a human reviews at each gate. The
rules below are the binding part. The playbook is background.

[AI-native SDLC playbook]: https://claude.com/resources/articles/the-ai-native-sdlc-playbook

Every feature starts in `changes/NNNN-<slug>/`:
`intent.md` (problem, outcome, users, constraints, open questions) ->
`spec.md` (requirements + design, includes the `docs/positioning.md` litmus-test check) ->
`plan.md` (from plan mode: files, order, risks, proof).
Do not implement before `plan.md` is accepted. If implementation drifts, update
`plan.md` in the same commit. Items in `docs/roadmap.md` are the backlog.

## Versioning / Compatibility

- API `fleet.babis.dev/v1alpha1`; breaking CRD changes are fine until v1beta1, but bump the version when they happen.
- Edge version: `edge/Cargo.toml`. Operator version: `-ldflags "-X main.Version=..."` (`make build VERSION=...`).
- Ingest auth is a shared bearer token in P1 (`BABIS_INGEST_TOKEN` operator, `BABIS_TOKEN` edges). Per-edge tokens before P3.

## Recurring mistakes

When Claude makes the same mistake twice, add the correction here.

- **Worktree subagents start from `main`, not the working branch** (happened in P1 steps 3 and 4). When you start an agent with `isolation: worktree`, tell it to run `git reset --hard <branch>` first and to check that the expected files exist; or let it work in the main checkout when nothing else runs there.

## Roadmap

See `docs/roadmap.md` (single source of truth; don't duplicate it).
