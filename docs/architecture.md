# Architecture

How Babis works, why it is shaped this way, and where the boundaries are.
This page is a summary. The design is in
[changes/0002-redesign/spec.md](../changes/0002-redesign/spec.md); read it
for detail. See [positioning.md](positioning.md) for scope.

Legend: **[implemented]** is built (phases P1 and P1b). **[planned]** is
agreed design that is not built. Phases are in [roadmap.md](roadmap.md).

## Topology [implemented]

```
 EDGE (each site)                           CENTRAL CLUSTER
┌──────────────────────────────┐           ┌───────────────────────────────┐
│ ArgoCD (autonomous)          │           │ babis-operator                │
│   ▲ read-only                │           │  ├─ ingest :8090              │
│   │                          │  HTTP(S), │  │    POST /api/v1alpha1/     │
│ babis checkin                │  outbound │  │    checkin  (server-side   │
│  (CronJob, runs, exits)  ────┼──────────►│  │    apply of Edge status)   │
│                              │  only     │  └─ Edge reconciler           │
│                              │           │       (conditions)            │
└──────────────────────────────┘           │ Edge CRs = fleet inventory    │
                                           │   `kubectl get edges`         │
                                           └───────────────────────────────┘
```

An edge without install rights has no `babis checkin`. Its ArgoCD
notifications controller posts to `POST /api/v1alpha1/app-events` instead.
See [Agentless path](#central-agentless-path-app-events-implemented).

Three invariants define the topology:

1. **Edges only dial out.** The operator never connects to an edge. There is
   nothing on an edge to connect to.
2. **Each edge is autonomous.** Its own ArgoCD pulls desired state from
   Git. Babis never carries manifests between clusters and never changes
   ArgoCD objects.
3. **The edge footprint is a one-shot CLI.** No daemon. Anything long-lived
   runs centrally.

## Components

### Edge: `babis checkin` [implemented]

A static Rust binary (musl). Source: `edge/`. It runs from the CronJob in
`deploy/edge/` and exits.

```
babis checkin --hub URL --edge-name NAME [--namespace argocd]
              [--hub-ca FILE] [--page-size 100] [--jitter 0s]
babis --version
```

The token is in the environment variable `BABIS_TOKEN`. Exit codes: `0`
sent, `1` Kubernetes or hub error, `2` usage error.

- **No Kubernetes client library.** The binary calls the API server with
  `ureq` and `serde`, decodes JSON as a stream, and lists with
  `limit`/`continue` pagination. Result: a small binary and low memory
  (budget: 3 MB binary, 5 MB peak RSS at 300 Applications; `make
  edge-budget`).
- **In-cluster credentials only** in P1 (service-account token and CA).
  Kubeconfig support is planned for P2.
- **Read-only.** The Role in `deploy/edge/rbac.yaml` has `get,list` on
  `applications`, `deployments`, `statefulsets` in the ArgoCD namespace and
  `get` on the secret `in-cluster` only. The e2e test checks that no write
  verb is allowed.
- **Observed per check-in** (a full snapshot): ArgoCD version (image tag of
  `argocd-application-controller`, else `argocd-server`), components
  (Deployments and StatefulSets with `app.kubernetes.io/part-of=argocd`:
  ready and desired replicas), Applications (name, sync, health,
  revision), and labels from the cluster secret `in-cluster` without
  reserved prefixes.
- **`--jitter`** waits a random time in `[0, DURATION)` before the
  check-in, so many CronJobs do not call the hub together. The CronJob uses
  `60s`.
- It shells out to nothing. (`helm` is for P2.)

### Central: operator [implemented]

A Go controller-runtime manager. Source: `central/`, entry
`central/cmd/babis-operator`. It has two members.

- **Ingest** (`central/internal/ingest`): `POST /api/v1alpha1/checkin`,
  `POST /api/v1alpha1/app-events` and `GET /healthz` on `--ingest-addr` (default `:8090`). Bearer-token auth,
  1 MiB body cap, strict validation. A check-in from an unknown edge
  **auto-registers** it (creates the Edge). Ingest writes status with
  **server-side apply** (field manager `babis-ingest`). It owns every
  status field except `conditions`, and never touches `spec` or
  `metadata.labels`.
- **Edge reconciler** (`central/internal/controller`): owns only the
  `conditions`, also with server-side apply (field manager
  `babis-reconciler`). It requeues at the moment the heartbeat could expire.

Flags that matter (see `central/cmd/babis-operator/main.go`):

| Flag | Default | Meaning |
|---|---|---|
| `--fleet-namespace` | `babis-fleet` | namespace of the Edge objects |
| `--ingest-addr` | `:8090` | check-in API address |
| `--heartbeat-timeout` | `15m` | default time without check-in before `Reachable=False` |
| `--max-edges` | `2000` | a new edge beyond this gets `429`; known edges keep working |
| `--max-inflight` | `64` | check-ins handled at once; others wait up to 20 s, then get `503` |
| `--leader-elect` | `true` | only the leader reconciles; every replica serves ingest |
| `--insecure-no-auth` | `false` | development only; without it an empty `BABIS_INGEST_TOKEN` stops the start |
| `--event-flush-interval` | `30s` | least time between two change flushes of one edge on one replica |
| `--event-touch-interval` | `5m` | flush an edge with unchanged states when its status is older than this |
| `--event-app-stale` | `45m` | remove an app (and a tombstone) with no event for this long |
| `--notifications-heartbeat-timeout` | `30m` | default `Reachable` timeout for notification edges |
| `--kube-api-qps`, `--kube-api-burst` | `-1`, `0` | client-side rate limit (off by default) |

`deploy/central` runs `replicas: 2` behind one Service. Ingest needs no
leader election, and server-side apply is safe with several writers.

### Central: agentless path (`app-events`) [implemented]

For edges where nothing can be installed. ArgoCD's own notifications
controller sends one request per Application event. The edge admin patches
two ArgoCD objects with `deploy/edge-notifications/`. Nothing from Babis
runs on the edge. Design: [0003 spec](../changes/0003-agentless-telemetry/spec.md).

- **Endpoint.** `POST /api/v1alpha1/app-events` on the ingest listener.
  Same bearer token. Body at most 64 KiB, one Application per request.
  The handler puts the event in a buffer and answers `202` with `{}`. It
  makes no API server call, because a slow hub blocks the edge's
  controller for every app. Errors: `401`, `400`, `413`, `429` (fleet
  full), `503` (no free slot in 2 s).
- **Buffer.** Each replica has its own buffer: edge, then app, then the
  newest event with its receipt time.
- **Flush.** One goroutine per replica. It flushes an edge after
  `--event-flush-interval` when a state changed, and after
  `--event-touch-interval` when only heartbeats arrived. A flush reads the
  `Edge`, merges the events by receipt time (an older event is skipped),
  prunes stale apps, recomputes the counts, and applies the status with
  server-side apply and a `resourceVersion` precondition. On `409` it
  reads again and retries (3 times, then the next interval). Two replicas
  can write one `Edge` safely.
- **Tombstones.** A `deleted` event removes the app and records it in
  `status.deletedApps`. An older `state` event from the other replica
  cannot add the app again. A tombstone lives for `--event-app-stale`.
- **Check-in wins while fresh.** Events for an edge whose source is
  `checkin` and whose last check-in is inside its heartbeat timeout are
  dropped.
- **Conditions.** `Reachable` uses the event age with the default timeout
  `--notifications-heartbeat-timeout`. `ArgoCDReady` is `Unknown`, reason
  `NotReported`. ArgoCD version, component state and identity labels are
  not available on this path.
- **Load.** 1000 edges with 300 apps at 500 requests per second: 0 errors,
  p99 3 ms, peak RSS about 147 MiB per replica (`SCENARIOS=C make load`).

### Edge CRD: `fleet.babis.dev/v1alpha1` [implemented]

Short name `edge`. Printer columns: `ARGOCD`, `READY`, `APPS`, `SYNCED`,
`HEALTHY`, `REACHABLE`, `LAST CHECKIN`.

- `spec` (written by humans): `displayName`, `heartbeatTimeout`.
- `status` (written from check-ins): `lastCheckinTime`, `labels`,
  `argocd{version, components[]}`, `apps[]` (with `lastSeen` on
  notification edges), `deletedApps[]`, `telemetry.source` (`checkin` or
  `argocd-notifications`), `appsTotal`, `appsSynced`, `appsHealthy`, and
  `conditions`. A `-o wide` listing adds the column `SOURCE`.
- `metadata.labels` are for admins (`kubectl label`). Ingest never changes
  them. `status.labels` is what the edge says about itself.

Conditions (pure functions in `central/internal/controller`, table-tested):

- **`Reachable`**: `True` when the last check-in (or event) is newer than
  the heartbeat timeout (default 15 min for check-ins, 30 min for
  notifications). `Unknown` before the first check-in. Reachability is
  inferred, never probed. Silence is the only signal that works with
  outbound-only edges.
- **`ArgoCDReady`**: `Unknown` while the edge is not `Reachable` (the last
  reported state may be stale). Otherwise `True` when every component has
  `ready == desired` and `desired > 0`; `False` with the names of the
  components that are not ready. Always `Unknown` (`NotReported`) for a
  notification edge.

CRDs, not a database, are the state store. History would be a separate,
additive consumer.

### Wire contract [implemented]

`wire/` holds the JSON Schemas (`checkin.request.schema.json`,
`checkin.response.schema.json`, `app-event.request.schema.json`) and
golden fixtures in `wire/testdata/` and `wire/app-event-testdata/`.
The Go tests (`central/internal/wire`) and the Rust tests
(`edge/tests/contract.rs`) run the same check-in fixtures. Only the Go
tests run the app-event fixtures, because the edge never sends them. CI fails on drift. See
[wire/README.md](../wire/README.md).

Rules:

- Additive changes only after the first release. Old edges must keep
  working with new operators.
- Unknown fields are ignored on both sides.
- The response has the reserved fields `generation`, `desired`,
  `extensions` (empty in P1), so P3 and P4 are not breaking changes.
- The edge name must be a DNS-1123 subdomain, because it becomes the
  object name.
- Errors: `401` bad token, `400` invalid payload, `413` body over 1 MiB,
  `429` fleet full (`--max-edges`), `503` busy (`--max-inflight`).

## ArgoCD coupling [implemented]

Babis is an ArgoCD tool by design (spec § ArgoCD coupling).

- ArgoCD only. No Flux, no generic GitOps layer.
- ArgoCD's own integration points first: Application status, the cluster
  secret, notifications, upstream manifests and chart.
- ArgoCD's vocabulary: `sync`, `health`, `revision` are sent untranslated.
- Babis supports the ArgoCD minor versions that upstream supports. e2e
  runs on the oldest and the newest (now v3.3.18 and v3.5.6).
- Babis never edits Applications, AppProjects, `argocd-cm` or
  `argocd-rbac-cm`.

## Scale [implemented]

The P1 baseline is 500 edges at the 5-minute interval. Nothing may block
5000. The risks are bursts and the write path, not volume. Measures:

- Edge `--jitter` spreads the calls.
- Ingest and reconciler write with server-side apply (one API call, no
  conflicts between the two writers).
- `--max-inflight` bounds the request bodies in memory. `--max-edges` caps
  what one leaked shared token can register. The count comes from the
  informer cache, so a burst of new edges can pass the cap by the few
  creates the cache has not seen yet. It is a guard, not an exact quota.
- `replicas: 2` for the operator.

Load test (`make load`, 1000 simulated edges, 300 apps each): with the
60-second spread the CronJob uses, 0 errors and p99 about 120 ms. In the
worst case (all edges at the same second) 0 errors, p99 about 5 s
(queueing on the API server) and operator peak RSS 117 MiB against a 192 Mi
limit. Details and the baseline are in
[plan.md § 6b](../changes/0002-redesign/plan.md).

[planned] For fleets far above 5000, the spec describes a heartbeat/status
split: write `Edge.status` only when it changed and keep the heartbeat in a
small `Lease`. It waits for a load run that shows the need.

## Security model

- P1 ingest auth is **one shared bearer token** (`BABIS_INGEST_TOKEN` on
  the operator, `BABIS_TOKEN` on edges), compared in constant time. This is
  accepted in P1 because central can change nothing on an edge. Per-edge
  tokens are the gate before P3 [planned].
- Babis does not terminate TLS. Put your ingress in front of `:8090`. The
  edge uses rustls with compiled-in web roots and `--hub-ca` for a private
  CA; `http://` works for an in-cluster hub. HTTPS against a real hub is
  not yet tested end to end.
- Check-ins are untrusted input: size-capped, decoded, and validated
  before any API call.
- A check-in can only influence the Edge object with its name. `spec` is
  not writable through ingest.
- The operator RBAC is generated (`central/config/rbac/`). The edge RBAC
  is read-only (see above).
- A leaked shared token lets an attacker register junk Edges up to
  `--max-edges` and fake state for known names. This is why per-edge tokens
  come before any feature that lets central change an edge.

## Failure modes

| Failure | Behavior |
|---|---|
| WAN down at check-in time | The one-shot fails (exit `1`) and the CronJob tries again at the next tick. A gap shorter than the timeout is invisible. A longer gap sets `Reachable=False` (and `ArgoCDReady=Unknown`). It heals at the next delivered check-in. |
| Operator down | Edges keep working on their own (autonomy). Check-ins fail until the operator returns. There is no edge-side queue. |
| ArgoCD component down on the edge | The next check-in shows it. `ArgoCDReady=False` names the components. |
| CronJob suspended or deleted | `Reachable=False` after the heartbeat timeout. |
| Duplicate edge names | Two edges write one Edge object. Prevented by naming practice now. Per-edge tokens will enforce it [planned]. |
| Clock skew on an edge | Harmless: `lastCheckinTime` is stamped by the operator when it receives the check-in. |
| Hub down or slow (notification edge) | The controller retries 3 times and gives up. The next 10-minute bucket sends the state again. ArgoCD sync is not affected. |
| Operator replica restarts | It loses its buffer (at most 30 s of changes or 5 min of heartbeats). The next bucket repairs it. |
| Fleet full (`--max-edges`) | New edges get `429`. Known edges keep working. |
| Burst over `--max-inflight` | Requests wait up to 20 s, then get `503` with `Retry-After`. The edge fails and retries at the next tick. |

## Repository layout

```
edge/                  Rust crate, binary `babis` (toolchain: rust-toolchain.toml)
central/               Go module github.com/BillyGrande/babis/central
  api/v1alpha1/        Edge types (kubebuilder markers)
  cmd/babis-operator/  operator entry
  cmd/babis-loadgen/   load generator for `make load`
  internal/wire/       request/response types and Validate()
  internal/ingest/     POST /api/v1alpha1/checkin and /app-events (buffer, flush, merge)
  internal/controller/ Reachability(), ArgoCDReadiness(), reconciler
  config/crd, rbac/    generated: never hand-edit
wire/                  JSON Schemas and golden fixtures (testdata/, app-event-testdata/)
deploy/edge/           kustomize: CronJob, ConfigMap, Role (install in the ArgoCD namespace)
deploy/edge-notifications/  kustomize patch for the ArgoCD notifications objects (no Babis install)
deploy/central/        kustomize: namespaces, CRD, RBAC, Deployment, Service
images/                Dockerfiles (prebuilt static binaries are copied in)
test/e2e/              kind end-to-end, oldest and newest ArgoCD
test/load/             load test driver (make load)
test/budget/           edge size and peak-RSS budget
docs/                  this file, positioning.md, roadmap.md
changes/               intent, spec, plan per change
```

## Planned phases

Short form. The details are in the spec and in [roadmap.md](roadmap.md).

- **P1b, agentless telemetry [implemented; release pending].** See
  [Central: agentless path](#central-agentless-path-app-events-implemented).
  Design: [0003 spec](../changes/0003-agentless-telemetry/spec.md).
- **P2, bootstrap [planned].** `babis bootstrap` installs ArgoCD with Helm
  (shell-out to `helm`), writes identity labels on the cluster secret
  `in-cluster`, and reads edge config from the ConfigMap `babis-edge`.
  Works air-gapped from a local OCI registry.
- **P3, desired state [planned].** Per-edge tokens first. Then
  `spec.argocd.chartVersion` and `spec.labels` travel in the check-in
  response; the edge applies only fields in its accept list (default:
  none). New condition `Converged`. This extends the scope to the lifecycle
  of the edge's ArgoCD installation, so `positioning.md` changes then.
- **P4, extensions [planned].** `extensions` in the wire format, edge
  plugins `babis-<name>`, one CRD per extension centrally. First: Hauler.
