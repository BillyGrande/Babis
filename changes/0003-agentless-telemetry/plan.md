# Plan: P1b phase B — build agentless telemetry

- **Status:** Accepted 2026-11-07 — in progress
- **Spec:** [spec.md](spec.md)

## Drift

- **Step 1:** `spike/combined.sh` copies the parts of `run.sh` that it
  needs, in place of a shared `spike/lib.sh`. So `run.sh` stays unchanged
  and does not need a 54-minute re-run.
- **Step 5:**
  - `make central-check` also runs `go test -race ./internal/ingest/`.
  - A failed flush (3 conflicts or another error) also waits one flush
    interval before the next try, so a broken API server is not called
    every second for every edge.
  - An idle edge leaves the buffer only when its last check is older than
    the flush interval, so the 30 s limit also holds for the next event.
  - `wire.MaxApps` is exported, and the buffer uses it.
  - Open risk: at the caps (2000 edges × 5000 apps) the buffer can use
    more than 1 GB per replica. The expected use (1000 × 300) is about
    10–30 MB. Step 8 measures it against the 192Mi memory limit.

## Context

The 0003 spec (`changes/0003-agentless-telemetry/spec.md`) is accepted and
merged (PR #7). Phase B builds what it describes:

- ArgoCD's notifications controller on an edge posts one event per
  Application to `POST /api/v1alpha1/app-events`.
- Central buffers the events on each replica and flushes them into `Edge`
  status. The flush merges with the current status, uses a
  `resourceVersion` precondition, and orders each app by its receipt time.
- The edge runs nothing from Babis.

Step 0 commits this plan as `changes/0003-agentless-telemetry/plan.md`.
Drift goes into it in the same commit as the change. Branch `p1b-build` from
`main` (07be172). One commit per step and one PR at the end.

## Steps

### 0. Plan file (main session)

Commit `changes/0003-agentless-telemetry/plan.md` (this plan). Link it
from the spec header.

### 1. Prove the combined `babis-state` trigger (runs in parallel with 2–4)

`changes/0003-agentless-telemetry/spike/combined.sh`. It reuses the install
and logger parts of `spike/run.sh`, factored into `spike/lib.sh` so that
`run.sh` stays as it is. It applies the spec config (one `babis-state`
trigger with the logger's `?trigger=state` path) on v3.5.6 and v3.3.18 in
parallel. It asserts:

- **a.** A new state is sent at once: scale a guestbook Deployment and see
  a request with the new state within 60 s.
- **b.** A revisited state is sent at the next bucket: go back to the old
  state. A request with the old state must arrive before the next 10-minute
  bucket starts plus 5 min.
- **c.** In a steady window of 20 min, there is at most 1 request per app
  per bucket.
- **d.** No `oncePer` errors in the controller log after the first minute.

Result: a new "Combined trigger" section in `spike.md`. If **a** or **b**
fails, I use the spec's fallback (the two-trigger config). I update the
spec's Config section and plan.md in the same commit, and I tell you
before step 6.

### 2. Wire

- `wire/app-event.request.schema.json`: `edge` (the same subdomain pattern
  as the check-in), `event` enum `state|deleted`, and `app.name` (1–253).
  `app.sync`, `app.health` and `app.revision` are `["string","null"]` with
  `maxLength: 253`. `additionalProperties` is allowed.
- `wire/app-event-testdata/`:
  - Valid: `request-state.json`, `request-first-event-nulls.json`,
    `request-bad-path.json` (`Unknown`/`Healthy`/`HEAD`),
    `request-deleted.json`, `request-unknown-fields.json`.
  - Invalid: `invalid-request-edge-missing`, `-edge-uppercase`,
    `-event-unknown`, `-app-missing`, `-app-no-name`, `-sync-too-long`,
    `-health-number`.
  - The bodies come from the real bodies in `spike.md`.
- `central/internal/wire/appevent.go`: `AppEventRequest{Edge, Event, App}`,
  `AppEventApp{Name, Sync, Health, Revision string}` (JSON `null` decodes
  to `""`), and `Validate()`. It reuses `validSubdomain` and `truncate`.
- `central/internal/wire/contract_test.go`: `TestContractAppEvent` reads
  `wire/app-event-testdata/`. It reuses `checkValid` and `checkInvalid`.
  The name matches `make contract` (`-run Contract`). Table cases go in
  `wire_test.go`.
- `wire/README.md`: the second fixture set, and why it is not in
  `testdata/` (both contract tests reject unknown entries there).
- Rust: no change.

### 3. CRD (`central/api/v1alpha1/edge_types.go`)

- Add `TelemetrySourceNotifications = "argocd-notifications"` and update
  the `Telemetry.Source` comment.
- Add `App.LastSeen *metav1.MicroTime` (`json:"lastSeen,omitempty"`).
- Add the printer column `SOURCE` (`.status.telemetry.source`,
  `priority=1`).
- Run `make generate manifests` and commit the generated files.

### 4. Reconciler (`central/internal/controller/`)

- `EdgeReconciler.NotificationsHeartbeatTimeout`. The timeout is
  `spec.heartbeatTimeout`, else the default for the source.
- `ArgoCDReady` is `Unknown`, reason `NotReported`, for the source
  `argocd-notifications`. `HeartbeatStale` still wins when the edge is
  unreachable.
- `main.go`: the flag `--notifications-heartbeat-timeout` (30 min).
- Table tests in `controller_test.go`: the timeout for each source, the
  override, and `NotReported`.

### 5. Ingest: endpoint, buffer, flush (Opus subagent; the hard part)

New files in `central/internal/ingest/`. `server.go` gets the route and the
`Config` fields.

- **`events.go`, the handler.** `AppEventsPath`.
  1. Auth (reuse `authorized`).
  2. An in-flight slot, with `EventInflightWait` (2 s), then `503` +
     `Retry-After: 5`.
  3. `MaxBytesReader` 64 KiB → `413`. Decode and `Validate` → `400`.
  4. An unknown edge at `--max-edges` (reuse `Index`) → `429`.
  5. `buffer.add` with receipt time `Now().UTC().Truncate(time.Microsecond)`,
     then `202 {}`. No API call. A drop because of a cap is also `202`,
     with a `V(1)` log.
- **`buffer.go`.** Mutex-guarded `map[edge]*edgeBuf`. An `edgeBuf` holds
  `entries map[app]entry` (event, sync, health, revision, received), plus
  `lastCheck` and `busy`.
  - On add, a newer entry replaces an older one.
  - The caps are 5000 apps per edge and `MaxEdges` edges.
  - `take(edge)` and `restore(edge, entries)` (restore keeps the newer
    entry per app).
- **`merge.go`, pure, with most of the tests.**
  - `mergeApps(cur []App, entries, now, stale) (apps []App, newest time.Time)`
    follows spec flush steps 3–5:
    - skip an entry with `received <= lastSeen`;
    - upsert or delete;
    - prune an app with `lastSeen < now-stale` or with no `lastSeen`;
    - sort by name.
  - `countApps`: reuse the logic from `buildApply` (move it into a helper
    for both paths).
  - `material(cur, entries) bool`: a new app, a delete, or a different
    sync, health or revision.
  - `checkinFresh(edge, checkinTimeout, now) bool`.
- **`flush.go`, the loop in `Server.Start`.**
  - A ticker (`FlushTick`, 1 s). For each edge with entries, not `busy`,
    and with `now-lastCheck >= EventFlushInterval`: dispatch it to a pool
    of `FlushWorkers` (8).
  - The worker reads the Edge from `Config.Reader` (the manager cache; main
    passes `mgr.GetClient()`). On `NotFound`: `Create` with the direct
    client, which tolerates `AlreadyExists`.
  - `checkinFresh` → drop the entries, with a `V(1)` log.
  - If not material and `lastCheckinTime > now-EventTouchInterval`: keep
    the entries and set `lastCheck = now`.
  - Else `take` and merge. Server-side apply with field manager
    `babis-ingest`, `ForceOwnership`, and a body with
    `metadata.resourceVersion`, `lastCheckinTime` (max of the current value
    and `newest`), `telemetry.source`, `apps` and the counts. The body has
    no `argocd` and no `labels`.
  - `409`: read again with the direct client. After 3 tries, `restore`.
    Other errors: `restore`, then log.
  - On shutdown: one best-effort flush of all edges, 5 s in total.
- **`Config`:** `Reader`, `EventFlushInterval` (30 s), `EventTouchInterval`
  (5 min), `EventAppStale` (45 min), `CheckinHeartbeatTimeout` (from
  `--heartbeat-timeout`), `EventInflightWait`, `FlushWorkers` and
  `FlushTick`. Only the spec's 4 flags are new in `main.go` (one of them is
  in step 4).
- **Tests.**
  - `merge_test.go`, table tests: upsert, older entry skipped, equal time
    skipped, delete, delete of an unknown app, prune by age, prune of apps
    without `lastSeen`, counts, sort, `material` cases, `checkinFresh`.
  - `events_test.go`, on the existing `fakeAPI` and `fakeIndex`: `202`,
    `401`, `400` (each rule), `413`, `429`, `503`, and no API call in the
    handler (a counting interceptor).
  - `flush_test.go`, a driven flush (call `flushOnce(ctx)`, no ticker):
    - a new edge is created with its apps and source;
    - two flushes merge;
    - a touch is skipped while fresh and written when stale;
    - check-in wins;
    - a `409` (interceptor) is retried and then succeeds;
    - 3 conflicts restore the buffer;
    - the conditions of the reconciler survive;
    - a check-in after notifications replaces the status.
  - Run `go test -race`.

### 6. Edge config `deploy/edge-notifications/` (after step 1)

- A kustomize **Component** (the same pattern as `deploy/edge/hub-ca/`).
  It has two strategic-merge patches: `argocd-notifications-cm.yaml` (the
  config from the spec or from step 1) and
  `argocd-notifications-secret.yaml` (`stringData.babis-token`).
- **Drift:** the edge name and the hub URL are inside YAML block scalars,
  and kustomize cannot set a value inside a string. So they are the
  placeholders `EDGE_NAME`, `HUB_URL` and `TOKEN`, which the admin replaces
  in a copy.
- `deploy/README.md` shows two ways: `components:` in the admin's own
  ArgoCD kustomization, or `kubectl patch --type merge --patch-file` for
  each object. It also shows a private CA: a key named after the hub host
  in `argocd-tls-certs-cm`. The notifications engine looks up a CA by
  server name (`SetCertResolver`, `pkg/util/http/transport.go`); the e2e
  proves it.
- `deploy/central/`: no change (the defaults are in code).

### 7. e2e scenario 7 (`test/e2e/run.sh`)

The scenario runs in the same cluster after scenario 6, with a second edge
name `e2e-notif`. The CronJob edge stays as it is. The operator gets these
extra args: `--event-flush-interval=5s`, `--event-touch-interval=30s` and
`--notifications-heartbeat-timeout=2m`.

1. Fill the placeholders with `sed` and apply the two patches with
   `kubectl patch`. Then wait for `e2e-notif` with source
   `argocd-notifications`, `appsTotal=3`, `Reachable=True` and
   `ArgoCDReady=Unknown` (`NotReported`).
2. Create an app `notif-app` with the resources finalizer. It appears.
   Delete it. It leaves (the `deleted` event).
3. Set the webhook URL to a name that does not resolve, and set
   `spec.heartbeatTimeout: 1m`. Wait for `Reachable=False`. Restore the
   URL and create an app (a new state). Wait for `Reachable=True`.
4. HTTPS: point the webhook at `https://babis.e2e.test` through Traefik
   from scenario 6. Add the CA to `argocd-tls-certs-cm` and a hostAlias on
   the notifications controller (test only). Create an app and see it
   arrive.

The e2e stays under about 15 min per version. If step 4 shows that
`argocd-tls-certs-cm` does not work, I record the real method (or "not
supported") in the spec and the README.

### 8. Load (`make load`, scenario C)

- `central/cmd/babis-loadgen`: `--mode events`, `--rate` (500/s), and
  `--hub` that can be given more than once (round robin). It sends each
  edge × app once as a `state` event, then prints the codes and the
  latency percentiles.
- `test/load/run.sh` scenario C:
  - It starts 2 operator processes on the host, with other ports and
    `--leader-elect=true`.
  - It reads `rest_client_requests_total{method="PATCH"}` from both
    metrics endpoints before and after, to get the status write rate.
  - Checks: 0 non-`202` answers, every Edge with
    `appsTotal == APPS` after one flush interval, and the VmHWM of each
    process.
  - Defaults: 1000 × 300 at 500/s, about 10 min.
- Record the result in `plan.md` and in the spec's load table (measured
  against the estimates).

### 9. Docs

- `README.md`: a short "Edges without install rights" section.
- `docs/architecture.md`: P1b is no longer planned.
- `docs/roadmap.md`: tick the P1b items.
- `deploy/README.md`: from step 6.
- `CLAUDE.md`: the new make or test notes, if there are any.

## Delegation

| Step | Who | Where |
|---|---|---|
| 0, reviews, integration, PR | Opus (main session) | main checkout |
| 1 combined-trigger proof | Sonnet subagent | worktree; `git reset --hard p1b-build` first |
| 2–4 wire, CRD, reconciler | Sonnet subagent | main checkout |
| 5 ingest buffer and flush | Opus subagent (concurrency and merge logic) | main checkout, after 2–4 |
| 6–7 deploy and e2e | Sonnet subagent | main checkout, after 1 and 5 |
| 8 load | Sonnet subagent | main checkout, after 5 |
| 9 docs | Sonnet subagent | after 7 and 8 |

Step 1 runs in parallel with 2–4, because it touches only `spike/` and
needs only kind. Every agent runs `make check` before its commit. I review
each diff before the next step starts.

## Risks

- **The combined trigger fails** (step 1). The fallback is in the spec. It
  costs the revisit delay and the duplicates.
- **SSA with `resourceVersion` on the status subresource.** The fake client
  may not enforce it. The unit tests use an interceptor, and e2e or load
  must show a real `409` retry in the operator logs (scenario C runs 2
  replicas against the same edges).
- **Private CA for the notifications controller** (step 7.4). The
  mechanism comes from reading the source code only.
- **The load test makes large writes** (300 apps in each apply, up to
  about 66 per second in scenario C). The API server in kind can be the
  limit. This is a measurement, not a pass gate, apart from 0 errors and
  correct final counts.

## Verification

- `make check` passes (including `go test -race` for ingest).
- `make contract`: the new `TestContractAppEvent` passes, and the Rust
  contract test is unchanged.
- `make generate manifests` leaves no diff.
- `bash changes/0003-agentless-telemetry/spike/combined.sh` passes on both
  versions.
- `make e2e` passes on v3.5.6 and v3.3.18, including scenario 7.
- `make load` scenario C: 0 errors, every Edge with 300 apps, and the write
  rate and memory recorded.
- `make edge-budget` is not needed (the edge code does not change).
- PR CI is green. Then you review the PR.
