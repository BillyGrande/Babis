# Spec: Babis core design (v0)

- **Status:** Accepted for P1 (2026-10-30). P1b–P4 sections are drafts until their own review.
- **Date:** 2026-10-30
- **Intent:** [intent.md](intent.md)

Deliberately small. Each phase below ships alone; later phases only add.
P1 targets edges that **already run ArgoCD**, so Babis is useful before it
installs anything.

## Overview

```
 EDGE (one-shot, CronJob or host cron)          CENTRAL (operator, any cluster)
┌────────────────────────────────┐            ┌──────────────────────────────┐
│ babis checkin  (every N min)   │            │ ingest  POST /checkin        │
│   observe ArgoCD (read-only)   │  outbound  │   observed  → Edge.status    │
│     ── POST ───────────────────┼───────────►│   Edge.spec → response (P3)  │
│   converge ◄── desired (P3) ───┼────────────┤                              │
│                                │  only      │ reconciler                   │
│ babis bootstrap (P2, day 0)    │            │   Reachable  (check-in age)  │
│   helm install ArgoCD          │            │   ArgoCDReady (components)   │
│   cluster secret + labels      │            │   Converged  (P3)            │
└────────────────────────────────┘            │ Edge CRs = fleet inventory   │
                                              └──────────────────────────────┘
```

**Concepts**

- **Edge** — one cluster with its own ArgoCD. One `Edge` object centrally.
- **Observed state** — `Edge.status`, written only from check-ins.
- **Desired state** (P3) — `Edge.spec`, written by humans or GitOps. Never
  by the edge.
- **Check-in** — one outbound request: the edge sends observed state; the
  response carries desired state (empty until P3).
- **Accept list** (P3) — the spec fields an edge lets central change.
  Default: none.

## Phases

| Phase | Ships | Gate |
|---|---|---|
| **P1 — Telemetry** | read-only `checkin` on existing ArgoCD edges; Edge CRD; operator image + deploy manifests | — |
| **P1b — Agentless telemetry** | ArgoCD's own notifications controller posts app events to central; nothing from Babis on the edge | P1 shipped |
| **P2 — Bootstrap** | `bootstrap` (ArgoCD via Helm) with identity labels and edge config | — |
| **P3 — Desired state** | per-edge tokens; `spec.argocd.chartVersion`, `spec.labels`; accept list; `Converged` | per-edge auth lands first |
| **P4 — Extensions** | `extensions` in the wire format; edge plugins; first: Hauler | P3 stable |

Later, not designed here: fleet rollout objects, imperative commands,
air-gap telemetry relays, Prometheus metrics.

## P1 — Telemetry

### Edge: `babis checkin`

**Read-only.** In P1 Babis writes nothing on the edge. It works on any
ArgoCD install: Helm, plain manifests, or operator-managed.

Run modes:

- **CronJob in the edge cluster** (recommended). Shipped as
  `deploy/edge/` (kustomize): CronJob with `concurrencyPolicy: Forbid`,
  ServiceAccount, read-only Role, Secret for the token. Nothing to install
  on the host.
- **Host cron / systemd timer** with a kubeconfig — **deferred to P2**.
  P1 uses in-cluster credentials only (service-account token + CA), which
  keeps the edge client small (see [Language decision](#language-decision)).

Configuration (flags, then env):

```
--hub URL            # required
--edge-name NAME     # default: lowercased hostname (CronJob: set explicitly)
--namespace NS       # ArgoCD namespace, default argocd
BABIS_TOKEN          # shared token (P1)
```

Observed, per check-in (full snapshot, not a delta):

| Field | Source |
|---|---|
| ArgoCD version | image tag of `argocd-application-controller` (fallback: `argocd-server`) |
| Components | each Deployment/StatefulSet with `app.kubernetes.io/part-of=argocd` in the namespace: name, ready/desired replicas |
| Applications | name, sync, health, revision (as the prototype) |
| Labels | the cluster secret `in-cluster`, if present, minus reserved prefixes (`argocd.argoproj.io/`, `babis.dev/`, `kubernetes.io/`, `k8s.io/`). Absent → none. |

No `report --label` flags: labels have one source. Edges without a cluster
secret get labels in P2 (`bootstrap`), or are grouped centrally (below).

Edge RBAC (read-only): `get,list` on `applications.argoproj.io`,
`deployments`, `statefulsets` in the ArgoCD namespace; `get` on secret
`in-cluster` only (`resourceNames`).

### Central: Edge CRD (P1 fields)

```yaml
apiVersion: fleet.babis.dev/v1alpha1
kind: Edge
metadata:
  name: shop-athens-042
  labels:                  # set by admins (kubectl label), never by ingest
    customer: acme
spec:
  displayName: Athens 42
  heartbeatTimeout: 15m
status:
  lastCheckinTime: "2026-10-30T10:00:00Z"   # stamped by central on receipt
  telemetry: {source: checkin}              # or argocd-notifications (P1b)
  labels: {region: gr-north, tier: retail}  # reported by the edge
  argocd:
    version: v3.2.0
    components:
      - {name: argocd-application-controller, ready: 1, desired: 1}
      - {name: argocd-repo-server, ready: 1, desired: 1}
      - {name: argocd-redis, ready: 1, desired: 1}
  apps: [...]
  appsTotal: 12
  appsSynced: 12
  appsHealthy: 11
  conditions:
    - {type: Reachable, status: "True"}
    - {type: ArgoCDReady, status: "True"}
```

```
$ kubectl get edges
NAME             ARGOCD    READY   APPS   SYNCED   HEALTHY   REACHABLE   LAST CHECKIN
shop-athens-042  v3.2.0    True    12     12       11        True        2m
kiosk-crete-113  v3.1.8    False   12     12       12        True        4m
plant-north-007  v3.2.0    True    9      9        9         False       4h
```

Two kinds of labels, never mixed:

- `status.labels` — what the edge says about itself (from its cluster
  secret).
- `metadata.labels` — what admins say about the edge, for
  `kubectl get edges -l customer=acme`. Ingest never touches them.

Removed from the core: `expectedStoreVersion`, `storeVersion`,
`registryHealthy`. They move to the Hauler extension (P4).

### Central: reconciler

Pure functions, table-tested:

- `Reachability(lastCheckin, timeout, now)` — unchanged from the
  prototype.
- `ArgoCDReadiness(components)` — `Unknown` while the edge is not
  `Reachable` (the reported state may be stale). Otherwise `True` when every component has
  `ready == desired` and `desired > 0`; `False` with the names of the
  components that are not ready. A missing report → `Unknown`.

### Central: deploy

- Operator container image (static binary, distroless).
- `config/` gains a Deployment, Service (`:8090`), and kustomization next to
  the generated CRD and RBAC.
- TLS: the user's ingress in front of `:8090` (documented, not built).

### P1 auth

Shared token, auto-registration on first check-in (as the prototype).
Acceptable because central can change nothing in P1.

### P1 done

- An existing edge (ArgoCD by Helm **or** plain manifests) applies
  `deploy/edge/` and appears in `kubectl get edges` with version,
  readiness, and app counts within one interval.
- Scale `argocd-repo-server` to 0 → `ArgoCDReady=False` on the next
  check-in.
- Suspend the CronJob → `Reachable=False` after the timeout; resume →
  `True` on the next check-in.
- No write verbs in the edge Role.

## P1b — Agentless telemetry (ArgoCD notifications)

For edges where nothing new can be installed — no CronJob, no RBAC, no
image pull — but the ArgoCD notification config can be edited. ArgoCD's own
`argocd-notifications-controller` posts per-Application events to central.
Babis runs nothing on the edge, so it adds **zero RAM** there. The check-in
API stays the full-featured path; this is the fallback.

Needs on the edge:

- `argocd-notifications-controller` running (part of the standard install;
  check minimal/core installs).
- Edit rights on `argocd-notifications-cm` and `argocd-notifications-secret`.

Babis ships `deploy/edge-notifications/`: a kustomize patch for those two
objects (edge name and hub URL as vars).

```yaml
# merged into argocd-notifications-cm
service.webhook.babis: |
  url: https://fleet.example.com
  headers:
  - name: Authorization
    value: Bearer $babis-token        # key in argocd-notifications-secret
  - name: Content-Type
    value: application/json
template.babis-app: |
  webhook:
    babis:
      method: POST
      path: /api/v1alpha1/app-events
      body: |
        {"edge": "shop-athens-042",
         "event": "{{if .app.metadata.deletionTimestamp}}deleted{{else}}state{{end}}",
         "app": {"name": {{toJson .app.metadata.name}},
                 "sync": {{toJson .app.status.sync.status}},
                 "health": {{toJson .app.status.health.status}},
                 "revision": {{toJson .app.status.sync.revision}}}}
trigger.babis-change: |
  - when: "true"
    oncePer: app.status.sync.status + app.status.health.status + app.status.sync.revision
    send: [babis-app]
trigger.babis-heartbeat: |
  - when: "true"
    oncePer: app.status.reconciledAt[0:15]    # ~1 event per app per 10 min
    send: [babis-app]
trigger.babis-deleted: |
  - when: app.metadata.deletionTimestamp != nil
    oncePer: app.metadata.deletionTimestamp
    send: [babis-app]
subscriptions: |
  - recipients: [babis]
    triggers: [babis-change, babis-heartbeat, babis-deleted]
```

**To verify on a real ArgoCD before this is final** (P1b e2e): the
`$babis-token` secret reference inside a header value, string slicing in
`oncePer`, `subscriptions` as a global (annotation-free) subscription, and
`toJson` on missing status fields.

Central:

- `POST /api/v1alpha1/app-events`, same bearer auth, body ≤ 64 KiB, one
  Application per event. Validation as the check-in (edge name, app name).
- **Coalescing.** Events go into a per-edge in-memory buffer; status is
  flushed at most every 30 s per edge (`--event-flush-interval`). Without
  this, 1000 edges × 300 apps would mean hundreds of API-server writes per
  second. A restart loses ≤ 30 s of events; the next heartbeat repairs it.
- Flush: upsert apps by name, set `apps[].lastSeen`, drop `deleted` apps
  and apps not seen for 3 heartbeat periods, recompute counts, set
  `lastCheckinTime` to the last receipt, set `status.telemetry.source:
  argocd-notifications`.
- If an edge also checks in, the check-in snapshot wins while it is fresh;
  events for that edge are ignored.

Compared with the check-in:

| | P1 check-in | P1b notifications |
|---|---|---|
| Install on the edge | CronJob + Role + image | edit 2 ArgoCD objects |
| Extra RAM on the edge | ~2 MB for < 1 s per run | 0 |
| ArgoCD version, component readiness | yes | no → `ArgoCDReady=Unknown` |
| Identity labels | cluster secret | optional static labels in the template; or admin `metadata.labels` |
| `Reachable` | check-in age | event age; needs ≥ 1 Application on the edge |
| Deleted apps | absent from the next snapshot | `deleted` event; stale-prune fallback |

## P2 — Bootstrap

`bootstrap` installs or converges ArgoCD via Helm and writes identity.

```
--chart, --chart-version, -f/--values, --plain-http   # as the prototype
--cluster-label k=v       # repeatable; identity labels
--edge-name NAME
--hub URL                 # optional; also installs the check-in CronJob
```

Identity labels:

- Source of truth on the edge: cluster secret `in-cluster` in the ArgoCD
  namespace (`argocd.argoproj.io/secret-type: cluster`, server
  `https://kubernetes.default.svc`).
- Absent → create. Present → merge. Never remove a label babis did not
  set. Works on a user-templated secret.
- Reserved prefixes are never settable. Invalid keys or values → error.

Edge config: ConfigMap `babis-edge` in the ArgoCD namespace (`hub`,
`edgeName`, chart source, later `accept`). `checkin` reads it; flags
override it.

With no `--hub`, the edge is standalone (air-gapped edges).

## P3 — Desired state

Outline only; a separate spec before it starts.

- Per-edge tokens first: admin creates the Edge; operator creates Secret
  `edge-<name>-token`; the edge name comes from the token, not the body.
  Auto-registration turns off.
- `spec.argocd.chartVersion`, `spec.labels`. The response carries them with
  `generation`.
- The edge applies only fields in its accept list (`bootstrap --accept`,
  default none) and reports per-field `Applied` / `Rejected` / `Failed` on
  the next check-in.
- ArgoCD upgrade: `helm upgrade --version <desired>
  --reset-then-reuse-values --rollback-on-failure` (Helm 4; Helm 3 security
  support ends 2026-11-11). Central sets the version only; chart source and
  values stay local.
- Central-set label keys are tracked in annotation
  `babis.dev/central-labels`; central removes only those.
- `Converged` condition: `observedGeneration == generation` and every
  desired field applied.

## P4 — Extensions

Outline only. Answer to "extra CRD or plugin": **both, one per side.**

- Edge: plugin executable `babis-<name>` on PATH (the `kubectl`/`git`
  model), desired JSON on stdin, observed JSON on stdout. Any language —
  hauler-helper can ship `babis-hauler`.
- Wire: `extensions.<name>` in request and response, opaque to the core.
- Central: each extension owns its own CRD and controller in its own API
  group (e.g. `EdgeStore` in `hauler.babis.dev`), keyed by edge name.

## Check-in API

`POST /api/v1alpha1/checkin`, `Authorization: Bearer <token>`.

Request (P1 fields):

```json
{
  "edge": "shop-athens-042",
  "labels": {"region": "gr-north"},
  "argocd": {
    "version": "v3.2.0",
    "components": [{"name": "argocd-repo-server", "ready": 1, "desired": 1}]
  },
  "apps": [{"name": "pos", "sync": "Synced", "health": "Healthy", "revision": "a1b2c3d"}]
}
```

Response `200` (P1: always empty):

```json
{"generation": 0, "desired": {}, "extensions": {}}
```

Errors: `401` bad token, `400` invalid payload, `413` body over 1 MiB.

`POST /api/v1alpha1/app-events` (P1b) — one Application per request, body
as in the P1b template; response `202` with an empty body. Its schema and
fixtures live in `wire/` with the check-in ones.

The API is public and documented: `babis checkin` and ArgoCD notifications
are two producers; a script or CI job that speaks the schema is a third.

Wire rules: additive only after the first release; unknown fields ignored
on both sides; `desired`, `generation`, `extensions` reserved now so P3 and
P4 are not breaking changes.

## Scale

Target: **500 edges** at the default 5-minute interval is the P1 baseline.
Nothing in the design may block **5000**.

Volume is small: 500 edges is 1.7 check-ins/s; with 300 apps per edge the
status is ~30 KB per Edge, so ~15 MB in etcd and in the operator cache. The
risks are bursts and the write path, not volume:

| Risk | Design (P1, plan step 6b) |
|---|---|
| Every CronJob fires in the same second (`*/5`). Client-side throttling is already off (controller-runtime sets QPS −1 and relies on API Priority and Fairness), so the burst queues on **API-server/etcd write throughput**: the baseline measured p99 8 s for 1000 simultaneous check-ins. | Edge `--jitter` (random delay before it observes; CronJob default 60 s). Fewer and smaller writes (rows below, and "Beyond 5000" below). `--kube-api-qps` / `--kube-api-burst` exist but stay disabled by default. |
| Each check-in does an uncached GET plus a diff-based merge patch: two API calls, and a stale or concurrent view can drop a change. | Ingest writes status with **server-side apply** (field manager `babis-ingest`), owning every status field except `conditions`. One call, no GET, correct under concurrent writers. On `404`, create the Edge, then apply. |
| One leaked shared token can register unlimited Edges and fill etcd. | `--max-edges` (default 2000): a new edge beyond the limit gets `429`; known edges keep working. Per-edge tokens stay the P3 gate. |
| One ingest replica. | Ingest needs no leader election and SSA is safe with several writers, so `replicas: 2` behind the Service. Only the leader reconciles. |
| No evidence. | `test/load/`: N simulated edges (default 1000, 300 apps each, jittered) against the kind operator. Pass: 0 errors, p99 < 1 s, operator RSS recorded. |

### Beyond 5000: the heartbeat/status split

The cost of a check-in is one write of the **whole** Edge object (etcd
stores and the watch sends full objects), ~30 KB with 300 apps — even when
only `lastCheckinTime` changed. Kubernetes solved the same problem for
nodes: Node status (large) is written only when it changes, and a tiny
`Lease` carries the heartbeat. Babis can do the same when the load test
says so:

- Ingest hashes the reported state; it writes `Edge.status` only when the
  hash changes.
- The heartbeat goes to a small per-edge object (a `coordination.k8s.io`
  `Lease` in the fleet namespace), which `Reachable` reads.
- The operator cache strips `status.apps` (cache transform), so memory grows
  with the number of edges, not edges × apps.

This cuts write bytes per steady-state check-in by ~100× and is the main
lever for very large fleets. Language and replicas are smaller levers: the
bottleneck is the API server, which neither changes.

Also later, only if measured: gzip request bodies.

## ArgoCD coupling

Babis is an ArgoCD tool, by design. It does not abstract over GitOps
engines.

- **ArgoCD only.** No Flux or generic GitOps support, and no abstraction
  layer that would allow it later.
- **ArgoCD's integration points first.** Application status (P1), the
  cluster secret → ApplicationSet cluster generator (P2), the
  notifications controller (P1b), the upstream install manifests and Helm
  chart (P2). A Babis-specific mechanism is added only where ArgoCD has
  none.
- **ArgoCD's vocabulary.** The wire format and the CRD use `sync`,
  `health`, `revision` with the values ArgoCD reports, untranslated.
- **Upstream version support.** Babis supports the ArgoCD minor versions
  that upstream supports. e2e runs against the oldest and the newest of
  them; ArgoCD's upgrade notes are read before a new minor is added.
- **Hands off ArgoCD's objects.** Babis code never edits Applications,
  AppProjects, `argocd-cm`, or `argocd-rbac-cm`. It writes only the
  cluster secret (P2) and the ArgoCD installation itself (P2/P3). The P1b
  notifications patch is applied by the edge admin, not by Babis.

## Language decision

**Edge: Rust. Central: Go.** One repo, two toolchains, one wire contract.

| Side | Language | Why |
|---|---|---|
| Edge `babis` | Rust | Low-RAM edges are a target. On the P1 job ([bench/](bench/README.md)): 2.2 MB binary, ~1.8 MB peak RSS at 300 Applications — vs 6.7 MB / 7.6 MB for the best Go variant. The edge work is plain HTTP + JSON, where Rust has no ecosystem gap. |
| Central operator | Go | controller-runtime (fake client, envtest, leader election), controller-gen (CRD **and** RBAC generation). kube-rs has no fake client and no RBAC generation. RAM does not matter centrally. |
| Edge plugins (P4) | Any (exec) | A Go `babis-hauler` can import Hauler v2 (`hauler.dev/go/hauler/v2`, already on Helm v4 + client-go). |

Rules that follow:

- **No Kubernetes client library on the edge.** No kube-rs, no client-go
  (45 MB and 58 MB peak on the same job). The edge uses ureq + serde,
  streaming decode, and paginated LIST (`limit=100`).
- **In-cluster credentials in P1.** Kubeconfig support (host cron) comes in
  P2.
- **Helm (P2/P3):** no Rust Helm library exists, so the edge shells out to
  `helm` v4. The P1 check-in image does not contain it.
- **One wire contract.** JSON Schema in `wire/`, plus golden fixtures that
  both the Go and the Rust test suites must decode and round-trip. CI fails
  on drift.
- **Maintenance.** Claude maintains both sides; the cost of two toolchains
  is CI time and contract tests.

Reverse the edge choice (to Go with streaming stdlib, 7.6 MB) if: calling
`helm` from the edge proves fragile, or the edge needs Kubernetes features
beyond GET/LIST/PATCH (watches, server-side apply conflict handling).

## Positioning check

| Litmus test | Result |
|---|---|
| 1. Hub dials an edge? | No. Everything rides on the edge's own request (check-in or ArgoCD notification). |
| 2. Moves manifests between clusters? | No. P3 sends a chart version and labels; the chart comes from the edge's own source. |
| 3. Encodes one org's naming? | No. Labels are free-form. |
| 4. Day-0 or fleet observability? | P1/P2 yes. P3 extends scope to the **lifecycle of the edge's ArgoCD installation** — `positioning.md` needs that update. |
| 5. Useful to argocd-agent users alone? | Yes: P1 monitors any ArgoCD; P2 bootstraps it. |

## Decisions vs the prototype

| Prototype decision | Now |
|---|---|
| A1/A3 scope: day-0 + observability | P1/P2 unchanged; P3 **extends** to the ArgoCD install lifecycle. Never applications. |
| B1–B4 outbound-only, autonomous, one-shot, inferred reachability | Kept. |
| C1 shell out to `kubectl`/`helm` | **Changed:** `checkin` calls the API server directly (Rust, no client library). `bootstrap` still shells out to `helm`. |
| G1 one Go module | **Changed:** Rust edge crate + Go central module, one wire contract. |
| C6 full snapshot per report | Kept. |
| D4 shared token, E2 auto-registration | Kept in P1/P2, **replaced in P3**. |
| E3 spec/status split | Kept. Admin labels go on `metadata.labels`. |
| `report --label` flags | **Removed.** Labels come from the cluster secret. |
| Store fields in the core CRD | **Moved** to the Hauler extension (P4). |
| `report` command | **Renamed** `checkin`. |

## Open questions

Resolved: `checkin` without `kubectl`, and the language — see
[Language decision](#language-decision).

1. Check-in interval default: 5 min (proposed in plan.md).
2. Report anything else about the installation in P1 (e.g. cluster
   Kubernetes version, node count)? Proposed: no — add later, additively.
