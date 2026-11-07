# Spec: agentless telemetry from ArgoCD notifications (P1b)

- **Author:** BillyGrande (draft by Claude)
- **Status:** Accepted 2026-11-07 (PR #7). Phase B: [plan.md](plan.md).
- **Intent:** [intent.md](intent.md)
- **Evidence:** [spike.md](spike.md) (ArgoCD v3.5.6 and v3.3.18 on kind)
- **Replaces:** [0002 spec § P1b](../0002-redesign/spec.md#p1b--agentless-telemetry-argocd-notifications)
  (draft)

## Summary

An edge where nothing can be installed sends its Application states to
central through ArgoCD's own notifications controller. The edge admin
merges one config into `argocd-notifications-cm` and one key into
`argocd-notifications-secret`. Central accepts the events on a new
endpoint, keeps them in memory per edge, and writes them to the `Edge`
status in batches. Babis runs nothing on the edge.

The spike changed the draft in five places:

1. **Events are hints. The heartbeat is the truth.** The controller loses a
   change event when the hub fails fast (it gives up after 4 attempts and
   then marks the event as sent). It also does not send a change again for
   a state that it sent before (`oncePer` keys stay in the annotation). So
   every app must send its full state at a fixed period, and central must
   not need any single event.
2. **One state trigger, not two.** The draft `babis-change` and
   `babis-heartbeat` triggers become one trigger, `babis-state`. Its
   `oncePer` key is the state plus a 10-minute time bucket. This sends a
   new state at once, sends the full state once per bucket, and repairs a
   revisited state at the next bucket. It also removes the duplicate
   requests that the spike saw when the two triggers fired together.
3. **Nil-safe expressions.** The draft `oncePer` expressions fail on an app
   with an empty status. A bad expression stops notifications for every app
   on the edge. The shipped expressions use `?.` and `??`.
4. **Central must answer at once.** The edge's notifications controller
   sends one request at a time. A slow hub blocks it for every app (the
   spike saw a 133 s hang). The endpoint returns `202` before any API
   server call.
5. **Coalescing works across two replicas.** A flush merges into the
   current `Edge` status with an optimistic-concurrency precondition, and
   each app entry is ordered by its receipt time.

## Requirements

- **R1.** An edge with only the notifications config appears as an `Edge`
  within one flush interval after its first event. It is auto-registered,
  as a check-in edge is, and it counts against `--max-edges`.
- **R2.** `status.apps` holds each Application with sync, health,
  revision and `lastSeen`. The counts (`appsTotal`, `appsSynced`,
  `appsHealthy`) agree with `status.apps`.
- **R3.** A state change on the edge shows in the status within
  `--event-flush-interval` (30 s) plus the send time, when no event is
  lost. A lost or revisited state shows within one heartbeat period
  (10 min) plus one ArgoCD reconcile (up to about 4 min) plus the flush
  interval.
- **R4.** `Reachable` is `True` while events arrive. It becomes `False`
  after `--notifications-heartbeat-timeout` (default 30 min) with no event.
  `spec.heartbeatTimeout` overrides it, as for check-in edges.
- **R5.** `ArgoCDReady` is `Unknown` with reason `NotReported` for an
  edge whose `status.telemetry.source` is `argocd-notifications`.
- **R6.** A deleted Application leaves the status: at once when ArgoCD
  sends a `deleted` event (the app has the resources finalizer), and after
  `--event-app-stale` (default 45 min) with no event in all other cases.
- **R7.** An edge that checks in keeps the check-in data. Events for an
  edge whose source is `checkin` and whose check-in is fresh are dropped.
- **R8.** At 1000 edges with 300 apps each, central accepts about 500
  requests per second over two replicas. The status writes stay near the
  P1 rate (a few per second when the fleet is steady).
- **R9.** P1 edges and the check-in API do not change. The CRD change is
  additive and stays `v1alpha1`.

## Edge setup

### Needs

- `argocd-notifications-controller` runs on the edge. It is in the
  standard install and in `core-install.yaml` (spike item 8).
- Edit rights on `argocd-notifications-cm` and
  `argocd-notifications-secret` in the ArgoCD namespace.
- Outbound HTTPS from the edge to the hub. A hub with a public
  certificate needs nothing more. For a hub with a private CA, phase B
  finds out how the notifications controller trusts it (not checked in
  the spike) and tests it in e2e.

### Config

Babis ships `deploy/edge-notifications/`. It is a kustomize directory that
patches the two objects. The inputs are the edge name, the hub URL and the
token. It does not replace other keys in the two objects: it is a strategic
merge patch, and the Babis keys all start with `babis`.

```yaml
# merged into argocd-notifications-cm
service.webhook.babis: |
  url: https://fleet.example.com
  headers:
  - name: Authorization
    value: Bearer $babis-token        # key babis-token in argocd-notifications-secret
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
trigger.babis-state: |
  - when: "true"
    # The state plus a 10-minute bucket of reconciledAt. A new state sends
    # at once; every app sends its state once per bucket.
    oncePer: >-
      (app.status.sync?.status ?? "") + "/" +
      (app.status.health?.status ?? "") + "/" +
      (app.status.sync?.revision ?? "") + "/" +
      (app.status.reconciledAt ?? "")[0:15]
    send: [babis-app]
trigger.babis-deleted: |
  - when: app.metadata.deletionTimestamp != nil
    oncePer: app.metadata.deletionTimestamp
    send: [babis-app]
subscriptions: |
  - recipients: [babis]               # the service name, not webhook:babis
    triggers: [babis-state, babis-deleted]
```

```yaml
# merged into argocd-notifications-secret
stringData:
  babis-token: <the shared ingest token>
```

What the spike proved, and what phase B must still prove:

| Part | Status |
|---|---|
| `$babis-token` in a header, `application/json`, POST | Proved (spike 1) |
| Global `subscriptions`, recipient `babis`, no app annotations | Proved (spike 3) |
| `toJson` on an empty status gives `null`, valid JSON | Proved (spike 4) |
| `deleted` with the finalizer; nothing without it | Proved (spike 5) |
| Nil-safe `oncePer` with `?.`, `??`, `[0:15]` | Proved for the separate heartbeat and change expressions (spike 2a) |
| The combined `babis-state` expression | Proved in phase B step 1 (`spike/combined.sh`, [spike.md § Combined trigger](spike.md#combined-trigger-phase-b-step-1)): a new state within 4 s, a revisited state 112–184 s after the next bucket start, at most 1 request per app per bucket when nothing changes |

The fallback, if a later ArgoCD version breaks the combined trigger, is the
two-trigger config from `spike.md` ("Corrected config"). It sends a
revisited state only with the next heartbeat, and it sends duplicates.

### Cost on the edge

- **CPU and RAM:** none from Babis. The notifications controller already
  runs.
- **Application writes:** each send writes the
  `notified.notifications.argoproj.io` annotation on the Application (spike
  item 6). With `babis-state` this is about 6 extra writes per app per
  hour, plus one per state change. The application controller writes each
  app at each reconcile (16–28 times per hour in the spike), so the extra
  load is about 20–40 %.
- **Annotation size:** each `oncePer` value adds one key (about 90 bytes).
  The notifications engine keeps the newest 100 keys
  (`notifiedHistoryMaxSize = 100`, `pkg/controller/state.go`; the same in
  the engine versions that ArgoCD v3.3.18 and v3.5.6 use). So the
  annotation stays below about 10 KB. This closes the open point in spike
  item 6.
- **Hub outage:** the controller retries 3 times (1–5 s apart), then gives
  up. While a connection hangs, it does not send for other apps (spike item
  7). ArgoCD sync itself is not affected. After the outage, each app sends
  its state at its next bucket.

## Central

### Endpoint

`POST /api/v1alpha1/app-events`, on the same listener as the check-in.

- **Auth:** the same shared bearer token (`BABIS_INGEST_TOKEN`). `401` if
  it is missing or wrong.
- **Body:** at most 64 KiB, else `413`. One Application per request.
- **Validation** (`400` on failure, with the reason):
  - `edge`: the same rule as the check-in edge name (DNS subdomain, at most
    253 characters).
  - `event`: `state` or `deleted`.
  - `app.name`: required, 1–253 characters.
  - `app.sync`, `app.health`, `app.revision`: a string or `null`, at most
    253 characters each. `null` is stored as an empty value.
  - Unknown fields are accepted and ignored, as in the check-in.
- **Limits:**
  - An unknown edge when the fleet has `--max-edges` edges: `429`.
  - The request takes an in-flight slot only while it reads the body. If no
    slot is free within 2 s, the answer is `503` with `Retry-After: 5`. The
    check-in waits up to 20 s; the event path must not hold the edge's
    controller that long.
- **Answer:** `202 Accepted` with the body `{}` after the event is in the
  buffer. The handler makes no API server call. A new edge is created by
  the flush, not by the handler.
- **Dropped events** (R7, an edge over the app cap): also `202`. The
  controller gains nothing from an error, and an error costs it retries.
  The drop is logged at `V(1)`.

### Buffer

Each replica has its own buffer. A load balancer can send two events from
one edge to two replicas, so neither replica has all events.

The buffer maps edge → app name → entry. An entry holds the event type,
sync, health, revision and the receipt time (from the replica clock, with
microseconds). A newer entry for the same app replaces an older one. The
buffer holds at most 5000 apps per edge (the check-in cap) and
`--max-edges` edges. An event above a cap is dropped (`V(1)` log).

### Flush

One goroutine per replica checks the buffer every second. It flushes an
edge when:

- **Change:** the buffer has a new app, a `deleted` entry, or an app whose
  sync, health or revision is different from the cached status, **and** at
  least `--event-flush-interval` (30 s) has passed since this replica's last
  flush for the edge; or
- **Touch:** the buffer has entries, and `status.lastCheckinTime` in the
  cache is older than `--event-touch-interval` (5 min).

A flush does this:

1. Read the `Edge` from the manager cache. If it does not exist, create it
   (`AlreadyExists` is fine) and use an empty status.
2. If `status.telemetry.source` is `checkin` and `lastCheckinTime` is within
   the heartbeat timeout of the edge: drop the buffer for the edge (R7) and
   stop.
3. Merge. For each buffered entry, find the app by name in `status.apps`:
   - The entry is older than or equal to the app's `lastSeen`: skip it.
     Another replica wrote a newer state.
   - `state`: set sync, health, revision and `lastSeen` (the receipt time).
     Add the app if it is not there.
   - The app has a tombstone in `status.deletedApps`, and a `state` entry
     is older than or equal to it: skip the entry. The app was deleted
     after this state, maybe through the other replica.
   - `deleted`: remove the app, and set its tombstone to the receipt time
     (also when the app is not in the list).
   - A `state` entry newer than the tombstone adds the app again and
     removes the tombstone (the app was created again).
4. Prune: remove each app whose `lastSeen` is older than `--event-app-stale`
   (45 min), and each app without `lastSeen` (it came from an old check-in).
   Remove each tombstone older than `--event-app-stale`.
5. Recompute the counts. Set `lastCheckinTime` to the newest receipt time
   in the merged entries (never older than the current value). Set
   `telemetry.source: argocd-notifications`.
6. Apply the ingest-owned status with server-side apply (field manager
   `babis-ingest`, as the check-in) and with `metadata.resourceVersion` from
   step 1. The API server refuses the apply with `409 Conflict` if the
   `Edge` changed after the read.
7. On `409`: read the `Edge` with the API reader (no cache) and go to step
   2. After 3 conflicts, keep the buffer and try again at the next check.
8. On success: remove the flushed entries from the buffer.

The apply does not set `status.argocd` or `status.labels`. Server-side
apply removes them, because the same field manager owned them. So an edge
that changes from check-in to notifications loses its ArgoCD version and
labels. Admin labels on `metadata.labels` are not touched.

Known limits, all repaired without action:

- **Delete race (fixed with tombstones).** Replica A buffers a `state`
  for an app; a heartbeat can wait up to the touch interval. Replica B
  then flushes a `deleted` for it. Without a tombstone, A's flush would add
  the app again until the stale prune. The e2e saw this in phase B, so the
  flush keeps a tombstone per deleted app in `status.deletedApps` for
  `--event-app-stale`.
- **Clock skew.** Receipt times come from two replica clocks. With a skew
  larger than the gap between two events, the older state can win until
  the next event. NTP keeps the skew far below the 30 s flush interval.
- **Restart.** A replica that stops loses its buffer (at most 30 s of
  changes, or 5 min of touches). The next bucket repairs it.

### Conditions

- `Reachable` uses `lastCheckinTime`, as today. The default timeout depends
  on `status.telemetry.source`: `--heartbeat-timeout` (15 min) for
  `checkin`, `--notifications-heartbeat-timeout` (30 min) for
  `argocd-notifications`. `spec.heartbeatTimeout` overrides both.

  Why 30 min: with one app on the edge, two events can be up to 10 min
  plus one reconcile apart (the spike saw 10.6 min). The touch interval
  adds up to 5 min. A 30 min timeout allows one lost bucket. An edge with
  zero Applications sends nothing and is never `Reachable`.
- `ArgoCDReady` is `Unknown`, reason `NotReported`, message "Edge reports
  through ArgoCD notifications; ArgoCD component state is not available",
  when the source is `argocd-notifications` (R5).

### API changes (`fleet.babis.dev/v1alpha1`, additive)

```go
// TelemetrySourceNotifications marks status written from ArgoCD notification events.
const TelemetrySourceNotifications = "argocd-notifications"

type App struct {
	// ... Name, Sync, Health, Revision as today
	// LastSeen is the receipt time of the last event for this app. Only
	// notification edges set it.
	// +optional
	LastSeen *metav1.MicroTime `json:"lastSeen,omitempty"`
}

// DeletedApps are apps removed by a `deleted` event. They are kept for
// --event-app-stale so that an older event from another replica does not
// add the app again. Only notification edges set it.
// +listType=atomic
// +optional
DeletedApps []DeletedApp `json:"deletedApps,omitempty"` // in EdgeStatus

type DeletedApp struct {
	Name      string           `json:"name"`
	DeletedAt metav1.MicroTime `json:"deletedAt"`
}
```

A check-in apply has no `deletedApps`, so server-side apply removes them:
a check-in is a full snapshot.

`status.apps` stays an atomic list. The check-in still sends the whole
list, and the flush sends the whole merged list.

`kubectl get edges` gets a new column `SOURCE`
(`.status.telemetry.source`), with `priority: 1` (shows with `-o wide`).

### Flags

| Flag | Default | Use |
|---|---|---|
| `--event-flush-interval` | 30 s | Least time between two change flushes of one edge on one replica |
| `--event-touch-interval` | 5 min | Flush an edge with only unchanged states when its status is older than this |
| `--event-app-stale` | 45 min | Remove an app with no event for this long |
| `--notifications-heartbeat-timeout` | 30 min | Default `Reachable` timeout for notification edges |

### Load at 1000 edges × 300 apps

| Measure | Value | Source |
|---|---|---|
| Requests per app per hour, steady | about 6 | One per 10-min bucket (spike 2a, 2b) |
| Requests at the hub, steady | about 500 per second | 1000 × 300 × 6 / 3600 |
| Requests for a state change | 1–2 per app | Spike surprise 2 (intermediate states) |
| Status writes, steady | about 3–7 per second | 1000 touches per 5 min, up to two replicas |
| Status writes, all edges changing | at most about 70 per second | 1000 per 30 s, up to two replicas |
| Status write size | ≤ 5000 apps × about 200 bytes | The check-in cap |
| Buffer memory per replica | about 10–30 MB, worst case | Up to 5 min of entries, about 100 bytes each |

P1 check-ins are 1000 per 5 min (about 3 writes per second). The touch
interval keeps notification edges near this rate. A request costs a
SHA-256, a JSON decode of a body below 1 KB and a map write. Phase B
proves the rate with `make load`.

**Measured** (`make load` scenario C, 2026-11-07, kind API server, two
replicas on one host). 1000 edges × 300 apps, each app sent once, at
500 requests per second, round robin over both replicas:

| Measure | Result |
|---|---|
| Answers | 300000 of 300000 `202`, 0 errors |
| Latency | p95 2 ms, p99 3 ms, max 110 ms |
| Status PATCH requests | 8458 in 642 s (13.2 per second), with 3425 `409` |
| Flushes that ran out of retries | 405; the next interval repaired all of them |
| Final state | 1000 of 1000 Edges with 300 apps and source `argocd-notifications`, 42 s after the last event |
| Peak RSS per replica | about 147 MiB (cache and buffer) |

This is the "all edges changing" case: every flush had new apps on both
replicas, so the replicas often wrote the same Edge at the same time. A
steady fleet has far fewer material flushes. The memory limit in
`deploy/central` is now 256Mi (147 MiB is under 75 %).

## Wire

- New schema `wire/app-event.request.schema.json`. It has the rules in
  "Endpoint". The answer has no schema (`{}`).
- New fixtures in `wire/app-event-testdata/`, with the prefixes
  `request-*` and `invalid-request-*`. The Go contract test reads them. The
  Rust contract test does not, because the edge never sends this request.
- The fixtures are not in `wire/testdata/`. Both contract tests read every
  entry of that directory and fail on any file or subdirectory that they do
  not know.
- The fixtures include the real first event of an app (all `null`), the
  bad-path event (`sync: Unknown`, `revision: HEAD`) and a `deleted` event,
  from `spike.md`.
- `wire/README.md` describes the second set.

No change to the check-in request or response.

## Compared with the check-in

| | P1 check-in | P1b notifications |
|---|---|---|
| Install on the edge | CronJob, Role, image | Patch 2 ArgoCD objects |
| Extra RAM on the edge | about 2 MB for less than 1 s per run | 0 |
| Extra writes on the edge | none | 1 Application annotation write per send |
| ArgoCD version, component readiness | yes | no; `ArgoCDReady=Unknown` |
| Identity labels | cluster secret | none; admin `metadata.labels` |
| `Reachable` | check-in age, 15 min default | event age, 30 min default; needs at least 1 Application |
| State delay | one CronJob interval | 30 s for a delivered change; up to about 15 min for a lost or revisited state |
| Deleted apps | absent from the next snapshot | `deleted` event with the finalizer; else after 45 min |

## Positioning check

The litmus tests in [positioning.md](../../docs/positioning.md):

1. **Does the hub dial an edge?** No. The edge's own controller sends
   outbound HTTPS. Central never connects to the edge.
2. **Does it move or sync manifests between clusters?** No. It reports
   Application state only.
3. **Does it encode one organization's naming or repo layout?** No. The
   edge name is an input, and the config uses only ArgoCD's own fields.
4. **Does it help fleet observability?** Yes. It is the observability path
   for edges where nothing can be installed.
5. **Do argocd-agent users benefit without adopting anything else?** Yes.
   An agent-managed cluster runs ArgoCD's notifications controller, so it
   can report with this config, with no Babis install.

It also follows the design rules: ArgoCD-only (ArgoCD's notifications
engine as it is), outbound-only, nothing resident from Babis on the edge,
CRDs as the state store, and one wire contract in `wire/`.

## Out of scope

- ArgoCD version, component readiness and cluster-secret labels for
  notification edges. The check-in gives them.
- Per-edge tokens. P1b uses the shared token, as P1 does (P3 gate).
- Any write to the edge from central.
- Delivery guarantees. Events are hints; the bucket send repairs them.

## Phase B outline

Phase B gets its own `plan.md` in plan mode after this spec is accepted.
The steps, in order:

1. **Prove `babis-state`.** Run `spike/run.sh` with the combined trigger
   and check the three items in "Config". If it fails, use the fallback
   config and update this spec.
2. **Wire.** `wire/app-event.request.schema.json`, the fixtures, a Go
   `AppEventRequest` type with `Validate()`, and the Go contract test.
3. **CRD.** `App.LastSeen`, `TelemetrySourceNotifications`, the `SOURCE`
   column. `make generate manifests`.
4. **Reconciler.** The default timeout per source, and `ArgoCDReady`
   `NotReported`. Table tests.
5. **Ingest.** The endpoint, the buffer and the flush. The merge is a pure
   function with table tests: upsert, older entry skipped, delete, prune,
   prune of apps without `lastSeen`, check-in wins, counts, the app cap.
   Handler tests on the fake client: `202`, `401`, `400`, `413`, `429`,
   `503`, and a flush that gets a `409` and retries.
6. **Deploy.** `deploy/edge-notifications/` and its README section. The
   operator flags in `deploy/central/`.
7. **e2e.** A new scenario with no CronJob: apply
   `deploy/edge-notifications/`, see the `Edge` with its apps; change a
   Deployment and see the change; delete an app with the finalizer and see
   it leave; stop the hub URL and see `Reachable=False` with a short
   `spec.heartbeatTimeout`. Also over HTTPS with the private CA of e2e
   scenario 6.
8. **Load.** `make load` gets an event mode at 500 requests per second
   over two replicas, and reports the status write rate.
9. **Docs.** README, `docs/architecture.md`, `docs/roadmap.md`.
