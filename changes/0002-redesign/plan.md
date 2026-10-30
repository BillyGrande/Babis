# Plan: P1 — Telemetry

- **Status:** Accepted 2026-10-30 — in progress (steps 1–6 done)
- **Date:** 2026-10-30
- **Spec:** [spec.md § P1](spec.md#p1--telemetry), [§ Scale](spec.md#scale), [§ Language decision](spec.md#language-decision)

## Progress

| Step | State | Commit |
|---|---|---|
| 1. Skeleton and toolchains | done | `a14359d` |
| 2. Wire contract | done | `2652941` |
| 3. Central (Go) | done | `a847e71` |
| 4. Edge (Rust) | done | `10d10bf` |
| 5. Deploy manifests and images | done | `2ed201a` |
| 6. End-to-end on kind | done | `84ef30f` + fixes |
| 6b. Scale hardening (added) | done | `50def5b`, `af0cfc2`, `6354811` |
| 7. CI | done | `c850f93` (PR #1) |
| 8. Docs | todo | — |

Drift from the original plan, recorded here as CLAUDE.md requires:

- **Strict DNS-1123** for edge names and label prefixes (step 2): the first
  draft accepted `a..b`, which Kubernetes rejects as an object name.
- **`ArgoCDReady` is `Unknown` while `Reachable=False`** (step 3): the last
  reported components of a silent edge may be stale. Spec updated.
- **Operator `/readyz`** was missing (found by the step 6 e2e): the Pod never
  became Ready. Fixed in `main.go`.
- **App counts always serialized** (no `omitempty`): `0` printed as an empty
  column in `kubectl get edges`.
- **ArgoCD versions pinned in e2e:** v3.5.6 (newest) and v3.3.18 (oldest
  supported), checked 2026-10-30.
- **Scale hardening (step 6b)** added after the 500+ edges review; see
  [spec § Scale](spec.md#scale).

Goal: a central hub that monitors existing ArgoCD installations. Edges run
a read-only Rust `babis checkin` from a CronJob; a Go operator turns
check-ins into `Edge` objects with `Reachable` and `ArgoCDReady`.

The prototype code is deleted, not migrated. `Reachability()` and its table
test may be copied over; nothing else is reused.

## Target layout

```
edge/                      Rust crate, binary `babis`
  Cargo.toml               ureq (rustls), serde, serde_json, lexopt; dev: tiny_http
  src/main.rs              args, exit codes
  src/kube.rs              in-cluster config, GET, paginated streaming LIST
  src/observe.rs           Applications, workloads, labels, version → Observation
  src/wire.rs              request/response types (serde)
  src/hub.rs               POST to hub
  tests/                   fixture + fake-API integration tests
central/                   Go module github.com/BillyGrande/babis/central
  api/v1alpha1/            Edge types (kubebuilder markers)
  cmd/babis-operator/      manager: reconciler + ingest runnable
  internal/wire/           request/response types + Validate()
  internal/ingest/         POST /api/v1alpha1/checkin
  internal/controller/     Reachability(), ArgoCDReadiness(), reconciler
  config/crd/, config/rbac/   generated (controller-gen) — never hand-edit
wire/
  checkin.request.schema.json
  checkin.response.schema.json
  testdata/                golden fixtures shared by Go and Rust tests
deploy/
  edge/                    kustomize: CronJob, ServiceAccount, Role, RoleBinding, Secret
  central/                 kustomize: namespace, CRD, RBAC, Deployment, Service
images/
  edge.Dockerfile          rust:1-alpine build → scratch
  central.Dockerfile       golang build → distroless/static:nonroot
test/e2e/run.sh            kind end-to-end
Makefile                   top level: check, build, images, e2e, generate, manifests
```

Deleted: `cmd/`, `internal/`, `api/`, `config/`, root `go.mod`/`go.sum`.

## Steps

Steps 3 and 4 are independent after step 2 and run in parallel (separate
worktrees). Every step ends with `make check` green.

### 1. Skeleton and toolchains

- Delete the prototype code (list above). Create the empty layout.
- Top-level `Makefile`: `check` = `central-check` (gofmt, vet, staticcheck,
  test) + `edge-check` (`cargo fmt --check`, `cargo clippy -D warnings`,
  `cargo test`); `build`; `generate`; `manifests`; `images`; `e2e`.
- Rust toolchain pinned in `edge/rust-toolchain.toml` (exact version,
  `rustfmt` + `clippy`, targets `x86_64-unknown-linux-musl` and
  `aarch64-unknown-linux-musl`). Local, CI and contributors all get the
  same toolchain from it. Release builds: `cargo zigbuild` for both musl
  targets (zig is the C compiler and linker, so `ring` builds without a
  musl toolchain).
- Dev machine (Arch): `pacman -S rustup zig cargo-zigbuild`; rustup reads
  the toolchain file.

**Proof:** `make check` passes on the empty skeleton.

### 2. Wire contract

- JSON Schemas for request and response, exactly the P1 fields in the spec
  plus the reserved response fields (`generation`, `desired`,
  `extensions`). `additionalProperties: true` everywhere (forward
  compatibility).
- Validation rules (in schema and in both implementations):
  - `edge`: DNS-1123 subdomain, ≤ 253 chars, required.
  - `labels`: Kubernetes label key/value syntax; reserved prefixes
    rejected.
  - `argocd.components[].ready/desired`: integers ≥ 0.
  - `apps`: ≤ 5000 entries; `name` required.
- `wire/testdata/`: `request-full.json`, `request-minimal.json`,
  `request-unknown-fields.json` (must be accepted), `response-empty.json`,
  `response-future.json` (with unknown `desired` keys — must be accepted),
  and `invalid-*.json` (one per rule).
- Go `internal/wire` and Rust `src/wire.rs`: types + `Validate`. Contract
  tests on both sides: every valid fixture decodes, validates, re-encodes,
  and decodes to the same value; every `invalid-*` fixture fails
  validation.
- Go test also validates the fixtures against the schemas
  (`github.com/santhosh-tekuri/jsonschema/v6`, test-only dependency).

**Proof:** both contract suites green on the same fixtures.

### 3. Central (Go)

- `api/v1alpha1`: `Edge` as in the spec (P1 fields only): `spec.displayName`,
  `spec.heartbeatTimeout`; `status.lastCheckinTime`, `labels`,
  `argocd{version, components[]}`, `apps[]`, `appsTotal/Synced/Healthy`,
  `conditions`. Printer columns: ARGOCD, READY, APPS, SYNCED, HEALTHY,
  REACHABLE, LAST CHECKIN. Short name `edge`. Then `make generate
  manifests`.
- `internal/ingest`:
  - `POST /api/v1alpha1/checkin`, `GET /healthz`.
  - Bearer token from `BABIS_INGEST_TOKEN`, constant-time compare. Empty
    token → refuse to start unless `--insecure-no-auth` (dev only).
  - Body cap 1 MiB (`413`); decode + `Validate` (`400`).
  - Unknown edge → create the `Edge` (auto-registration). Then patch status
    (`MergeFrom`): everything except `conditions`. `lastCheckinTime` =
    receipt time.
  - Response: `{"generation":0,"desired":{},"extensions":{}}`.
  - Never touches `spec` or `metadata.labels`.
- `internal/controller`:
  - `Reachability(lastCheckin, timeout, now)` — copy from prototype.
  - `ArgoCDReadiness(components)` — `True` iff non-empty and every
    component has `desired > 0 && ready == desired`; `False` lists the
    components that are not ready; no check-in yet → `Unknown`.
  - Reconciler owns only `conditions`; requeues at the heartbeat deadline.
- `cmd/babis-operator`: flags `--fleet-namespace` (default `babis-fleet`),
  `--ingest-addr` (`:8090`), `--heartbeat-timeout` (`15m`), leader
  election on.
- Tests (table-driven, fake client, no cluster): both pure functions;
  ingest handler — auth, size cap, invalid payload, auto-register, update,
  conditions untouched, metadata labels untouched.

**Proof:** `make central-check`; generated files committed; `kubectl get
edges` columns visible via `kubectl apply --dry-run=server` against kind
in step 6.

### 4. Edge (Rust)

- CLI: `babis checkin --hub URL --edge-name NAME [--namespace argocd]
  [--hub-ca FILE] [--page-size 100]`, token from `BABIS_TOKEN`.
  `--edge-name` is **required** (in a pod, the hostname is the pod name).
  `babis --version`. Argument parsing with `lexopt`.
- `kube.rs`: in-cluster config from `KUBERNETES_SERVICE_HOST/PORT`,
  `/var/run/secrets/kubernetes.io/serviceaccount/{token,ca.crt}`. Re-read
  the token per run (projected tokens rotate). Paginated LIST with
  `limit` + `continue`; decode each page with a streaming reader
  (`BufReader` 64 KiB) into minimal structs.
- `observe.rs`:
  - Applications in `--namespace`: name, sync status, health status,
    revision.
  - Deployments + StatefulSets with `app.kubernetes.io/part-of=argocd`:
    name, `spec.replicas` (default 1 if absent), `status.readyReplicas`
    (default 0).
  - Version: tag of the first container image of
    `argocd-application-controller` (StatefulSet or Deployment), fallback
    `argocd-server`. Digest-only image → `unknown`.
  - Labels: secret `in-cluster`; `404` → no labels; reserved prefixes
    removed.
- `hub.rs`: POST with bearer token; TLS via rustls with webpki roots
  compiled in (the image has no CA store), plus `--hub-ca`. `http://`
  allowed (in-cluster hub, dev).
- Exit codes: `0` sent; `1` hub or Kubernetes error (message on stderr);
  `2` usage error. One JSON log line per run on stdout.
- Release profile: `lto = true`, `codegen-units = 1`, `panic = "abort"`,
  `strip = true`.
- Tests: unit tests on bench-style fixtures (Applications, workloads,
  digest images, missing fields); integration test with `tiny_http` fake
  API + fake hub (pagination across 3 pages, `404` secret, hub `401`).

**Proof:** `make edge-check`; static musl binary ≤ **3 MB**; peak RSS ≤
**5 MB** at 300 Applications, measured with
[bench/harness.py](bench/README.md) against the real binary.

### 5. Deploy manifests and images

- `deploy/edge/`: CronJob `babis-checkin` (`*/5 * * * *`,
  `concurrencyPolicy: Forbid`, `successfulJobsHistoryLimit: 1`,
  `failedJobsHistoryLimit: 3`, `activeDeadlineSeconds: 120`); resources
  requests `cpu: 10m, memory: 16Mi`, limit `memory: 32Mi`; non-root,
  read-only root FS, no privilege escalation. ServiceAccount + Role:
  `get,list` on `applications.argoproj.io`, `deployments`,
  `statefulsets`; `get` on `secrets` with `resourceNames: [in-cluster]`.
  Secret `babis-token`. Edge name and hub URL as kustomize vars.
- `deploy/central/`: namespace `babis-system`, fleet namespace
  `babis-fleet`, CRD, RBAC (generated), Deployment, Service `:8090`.
- Images (`make images`): edge from `scratch` (amd64 + arm64); central on
  `distroless/static:nonroot`.

**Proof:** `kubectl apply --dry-run=server -k deploy/edge` and `-k
deploy/central` succeed on kind; edge image ≤ 4 MB.

### 6. End-to-end on kind

`test/e2e/run.sh` (also `make e2e`), one kind cluster per ArgoCD version —
the oldest and the newest minor that upstream supports (pinned in the
script, see [spec § ArgoCD coupling](spec.md#argocd-coupling)):

1. Install ArgoCD from pinned upstream manifests; create 3 Applications.
2. Build and `kind load` both images; apply `deploy/central` with
   `--heartbeat-timeout=2m`; apply `deploy/edge` with schedule
   `* * * * *` and hub `http://babis-operator.babis-system:8090`.
3. Assert (with timeouts):
   - Edge appears with the ArgoCD version, `appsTotal=3`,
     `ArgoCDReady=True`, `Reachable=True`.
   - Scale `argocd-repo-server` to 0 → `ArgoCDReady=False`.
   - Suspend the CronJob → `Reachable=False` within 4 min; resume →
     `True`.
   - The edge Role has no write verbs.

**Proof:** script exits 0 locally and in CI.

### 6b. Scale hardening (500+ edges)

From [spec § Scale](spec.md#scale). In this order — the load test first, so
every fix has a before/after number:

1. `test/load/`: Go load generator, N simulated edges (default 1000, 300
   apps each), jittered over one interval, against the kind e2e operator.
   Reports p50/p99 latency, error count, and operator RSS. `make load`.
   Run it on the current code and record the baseline.
2. Ingest writes status with server-side apply (field manager
   `babis-ingest`; no GET; on 404, create the Edge and apply again).
   Handler tests: concurrent check-ins for one edge, removed label
   disappears, conditions untouched.
3. Client flags `--kube-api-qps` and `--kube-api-burst` on the rest config
   (manager and ingest client). Defaults -1 / 0 = client-side limiting
   disabled (controller-runtime's own default); a positive QPS enables it.
4. `--max-edges` (2000): a new edge beyond the limit gets `429`, existing
   edges keep working. Handler test.
5. Edge `--jitter DURATION` (CLI default 0; CronJob default `60s`; job
   `activeDeadlineSeconds` 180). Unit test: delay is in `[0, jitter)`.
6. `deploy/central`: `replicas: 2`; e2e still passes.
7. (Added after the baseline.) Bounded ingest concurrency: `--max-inflight`
   (default 64) is taken **before** the body is read, so at most that many
   bodies are in memory; a request that waits longer than 20 s gets `503`
   with `Retry-After`. The baseline burst peaked at 299 MB against a 256 Mi
   limit. Set the memory limit from the measured "after" numbers.
8. (Added after the baseline.) The reconciler also writes with server-side
   apply (field manager `babis-reconciler`, owns only `conditions`), which
   removes the "object has been modified" conflicts seen in the baseline.

**Proof:** `make load` at 1000 edges, numbers before and after recorded in
this plan:

- Scenario B (spread 60 s — the deployed CronJob jitter): 0 errors, p99 < 1 s.
- Scenario A (burst — the worst case, every edge with jitter 0): 0 errors
  (no 5xx, no timeouts) and operator peak RSS below 75 % of its memory limit
  in `deploy/central`. Latency in A is queueing by design (one API call per
  check-in under a client rate limit) and is reported, not gated.

(Revised after the baseline: the first draft gated A on p99 < 1 s, which no
rate-limited write path can meet for 1000 simultaneous requests.)

#### Load test results

Location drift: the generator lives in `central/cmd/babis-loadgen`, not
`test/load/`, because it imports `central/internal/wire`. `test/load/run.sh`
drives it (`make load`). The script builds only the operator and the
generator (not the edge) and starts a fresh operator per scenario, so
`VmHWM` and the log count are per scenario.

**Baseline** (before any 6b fix). 2026-10-30, commit `0b011e6`, WSL2 +
kind (operator on the host, kind API server), 1000 edges, 300 apps each,
2 rounds, 30 s client timeout. Run time about 10 min (mostly deleting the
Edges between scenarios).

| Scenario | Round | OK / errors | p50 | p99 | max | Wall | Edges | Operator VmHWM |
|---|---|---|---|---|---|---|---|---|
| A burst (spread 0) | 1 | 1000 / 0 | 3279 ms | 8058 ms | 8106 ms | 8.1 s | 1000 | 299 MB |
| A burst | 2 | 1000 / 0 | 2825 ms | 5422 ms | 5460 ms | 5.4 s | 1000 | 299 MB |
| B spread 60 s | 1 | 1000 / 0 | 38 ms | 112 ms | 139 ms | 60.0 s | 1000 | 108 MB |
| B spread 60 s | 2 | 1000 / 0 | 24 ms | 82 ms | 101 ms | 59.9 s | 1000 | 108 MB |

Findings:

- The burst gave no request errors (all 200), but latency is 3-8 s. This is
  API-server/etcd write throughput: controller-runtime v0.25.2 already
  disables client-side throttling (QPS -1 when unset; it relies on API
  Priority and Fairness), so there is no client limiter. The 30 s timeout
  was not reached. Burst p99 is far above the 1 s target; spread p99 is
  already below it.
- The operator logged 7 `Reconciler error` lines in A (0 in B): `the object
  has been modified` conflicts on the Edge status update, and one `context
  canceled` at shutdown. They are retried and harmless, but show the
  reconciler and ingest compete for the same status.
- `VmHWM` of 299 MB in the burst comes from caching 1000 Edges with 300 apps
  each plus 1000 buffered request bodies. Watch it after SSA (step 2).

**After** (step 6b, 2026-10-30, same setup as the baseline: 1000 edges,
300 apps, 2 rounds, 30 s client timeout; ingest and reconciler on
server-side apply, `--max-inflight 64`, client limiting disabled).

| Scenario | Round | OK / errors | p50 | p99 | max | Wall | Edges | Operator VmHWM |
|---|---|---|---|---|---|---|---|---|
| A burst (spread 0) | 1 | 1000 / 0 | 2367 ms | 4480 ms | 4540 ms | 4.5 s | 1000 | 117 MiB |
| A burst | 2 | 1000 / 0 | 2949 ms | 5658 ms | 5683 ms | 5.7 s | 1000 | 117 MiB |
| B spread 60 s | 1 | 1000 / 0 | 38 ms | 122 ms | 180 ms | 60.0 s | 1000 | 107 MiB |
| B spread 60 s | 2 | 1000 / 0 | 30 ms | 115 ms | 153 ms | 60.0 s | 1000 | 107 MiB |

The baseline column "VmHWM" was in MB (kB/1024, so MiB); same unit here.

- Reconciler errors: 0 in B; 1 in A, a `context canceled` at shutdown (the
  baseline's 7 `object has been modified` conflicts are gone).
- Proof: B 0 errors, p99 122 ms < 1 s: met. A 0 errors, peak 117 MiB =
  61 % of the 192 Mi limit (< 75 %): met. A latency (p99 about 5 s) is
  queueing on API-server write throughput and is reported, not gated.
- Memory limit: 192Mi (117 / 0.75 = 156, rounded up), request 128Mi.
- Defaults: `--max-inflight 64` and `--max-edges 2000` as planned.
  `--kube-api-qps` -1 / `--kube-api-burst` 0 (limiting off): with the
  limiter on, a 1000-edge burst at 50 QPS exceeded the 20 s inflight wait and
  returned 503 (first trial, before this default was changed); the
  limiter only adds queueing in front of the API server's own limits.
- Burst peak RSS fell from 299 MB to 117 MiB: the in-flight bound and no
  per-request GET/object copy. In the kind setup the load generator and
  operator share the host with the API server, so absolute latencies are
  pessimistic.

**6b follow-ups** (not blocking P1):

- `--max-edges` counts from the informer cache, which can lag a few creates,
  so a burst of new edges can overshoot the cap slightly. Document it in the
  flag help and `deploy/README.md` (step 8).
- `make load` spends ~7 of its ~10 min in `kubectl delete edges --all`.
  Recreate the `babis-fleet` namespace between scenarios instead.
- The heartbeat/status split ([spec § Beyond 5000](spec.md#beyond-5000-the-heartbeatstatus-split))
  waits for a 5000-edge load run that shows the need.

### 7. CI

`.github/workflows/ci.yml` jobs: `central` (Go check + generated-files
drift), `edge` (fmt, clippy, test, musl build, size budget), `contract`
(both suites on `wire/testdata`), `e2e` (kind; on PRs and `main`).

**Proof:** all jobs green on the PR.

### 8. Docs

- `README.md`, `docs/architecture.md`, `docs/roadmap.md`: new phases, Rust
  edge + Go central, check-in API, `deploy/` quickstart.
- `CLAUDE.md`: layout, commands, design rules (no Kubernetes client on the
  edge; shell out only to `helm`; one wire contract), verification for both
  toolchains.
- `docs/positioning.md`: unchanged in P1 (scope change comes with P3).

**Proof:** every command in README quickstart runs as written on kind.

## Next: P1b (after P1 ships)

Not part of this plan's done criteria; listed so P1 does not block it.

1. **Spike first:** on kind with ArgoCD, apply the P1b notification config
   against a request-logging HTTP server. Confirm the four items marked "to
   verify" in the spec. Adjust the template before any central code.
2. Wire: `app-event` schema + fixtures in `wire/` (Go side only — no Rust).
3. Central: `POST /api/v1alpha1/app-events`, per-edge coalescer with flush
   interval, `apps[].lastSeen`, stale prune, `status.telemetry.source`.
   Table tests for the coalescer (upsert, delete, prune, check-in wins).
4. `deploy/edge-notifications/` kustomize patch.
5. e2e: second scenario in `test/e2e/run.sh` with no CronJob — Edge appears
   from events; delete an Application → it leaves `status.apps`.

## Risks

| Risk | Mitigation |
|---|---|
| Two languages drift on the wire | Shared fixtures and schemas in CI (step 2) |
| Large Applications blow edge memory | Streaming decode + pagination; RSS budget checked in CI (step 4) |
| ArgoCD "apps in any namespace" setups | P1 lists `--namespace` only; documented limitation |
| Digest-pinned ArgoCD images | Version `unknown`; consider adding `image` to the wire later (additive) |
| Ingest and reconciler both patch status | Ingest never writes `conditions`; reconciler writes only `conditions` |
| `ring` needs a C toolchain for musl builds | `build-base` in the builder image; `cargo-zigbuild` for cross |
| Rust toolchain missing locally | rustup with approval, or docker builds |
| Shared token leaks | Accepted for P1: central can change nothing; `--max-edges` caps junk registrations (6b); per-edge tokens before P3 |
| Synchronized CronJobs at 500+ edges | Edge `--jitter`, ingest QPS flags, SSA writes (6b) |
| Worktree subagents start from `main`, not the working branch | Tell them to reset to the branch first (happened twice; see CLAUDE.md) |

## Out of scope for P1

Host cron / kubeconfig mode, `bootstrap`, `apply`, desired state, per-edge
auth, extensions, metrics, release automation and signing.

## Delegation

| Step | Who |
|---|---|
| 1, 7 | Sonnet subagent |
| 2 | Sonnet subagent; main session reviews the schemas |
| 3 (Go) and 4 (Rust) | two Sonnet subagents in parallel worktrees |
| 5, 6 | Sonnet subagent |
| 6b | Sonnet subagent(s): load test first, then central and edge in parallel |
| 8 | Sonnet subagent |
| Review, budgets, e2e verification, integration | main session (Opus) |
