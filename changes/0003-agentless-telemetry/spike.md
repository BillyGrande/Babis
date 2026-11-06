# P1b spike: ArgoCD notifications as agentless telemetry

- **Date:** 2026-11-06
- **ArgoCD versions:** v3.5.6 and v3.3.18 (kind, `install.yaml` from upstream)
- **Base commit:** 1769779 (branch `p1b-spike`)
- **Script:** `spike/run.sh` (not wired into make or CI). The final script version ran end to end on both versions in one run.
- **Run time:** v3.5.6: 3210 s (15 min heartbeat window). v3.3.18: 2716 s (6 min window). Both ran in parallel, so the whole run took about 54 min.
- **Instrumentation:** the spike uses three templates (`babis-change`, `babis-heartbeat`, `babis-deleted`) with the same body. Each template has `?trigger=<name>` in its path. This only lets the logger tell the triggers apart. The body has one extra field, `reconciledAt`. The shipped config uses one template (see "Corrected config"). That one-template variant was not run.

## Summary

| # | Item | Result | Impact on the spec |
|---|------|--------|--------------------|
| 1 | `$babis-token` in a header value | Works. 100 % of requests carry `Authorization: Bearer <secret>`. | None. |
| 2a | `oncePer` with `reconciledAt[0:15]` | Parses and fires on a new 10 min bucket. But the draft expression errors while status is empty. | Use nil-safe expressions. |
| 2b | Real heartbeat rate | `[0:16]` gives one event per app per reconcile: 122-260 s apart, 16-28 events per app per hour. | `[0:15]` gives about 6 per app per hour. Plan the hub request rate from this. |
| 3 | Global `subscriptions` | Works. Events arrive for all apps. No annotation on the Applications. | None. The recipient is `babis` (the service name). |
| 4 | `toJson` on missing fields | Valid JSON, fields are `null`. 0 invalid bodies in all runs. | Hub must accept `null` for sync, health, revision. |
| 5 | Deletes | App with finalizer: `deleted` arrives at once. App without finalizer: nothing arrives. | Stale prune is the main delete path. |
| 6 | Edge cost | Each send writes the `notified.notifications.argoproj.io` annotation. One extra Application write per send. The annotation grows by one key per `oncePer` value. | Real cost on the edge. Prune behaviour is open. |
| 7 | Hub down | Slow failure (2 min): event arrives late. Fast failure (DNS): the change event is lost. The controller blocks while it waits. | Heartbeat is the repair path. Do not rely on change events. |
| 8 | Notifications controller in core install | Present in both versions. | None. |
| + | Revisit of an old state | The change trigger does not fire again for a state that was sent before. | Breaks the draft `babis-change` design. See item "Surprises". |

## Item 1. `$babis-token` inside a header value

**Result:** works on both versions. The controller replaces `$babis-token` with the value from `argocd-notifications-secret`.

**Evidence** (v3.5.6; v3.3.18 is the same: 32 of 32):

```
spike token: spike-98da9d2963601c23c2b0d106
requests with Authorization == 'Bearer <token>': 34 of 34
Content-Type values: {'application/json': 34}
headers of first request: {"Host": "hub-logger.babis-spike.svc:8080", "User-Agent": "Go-http-client/1.1", "Content-Length": "143", "Authorization": "Bearer spike-...", "Content-Type": "application/json", "Accept-Encoding": "gzip"}
```

**Impact:** none. The Content-Type header is `application/json`, as the draft sets. The method is POST. The body ends with a newline (from the YAML block scalar).

## Item 2. `oncePer` with string slicing and the heartbeat rate

### 2a. Does `[0:15]` parse and fire?

**Result:** yes, with a nil-safe expression. The draft expression `app.status.reconciledAt[0:15]` also parses, but it fails while `status` is empty or partly filled.

**Evidence, draft expressions, controller log (same count on both versions: 34 errors in the first minutes):**

```
10 failed to execute oncePer condition: cannot fetch reconciledAt from <nil> (1:12)
10 failed to execute oncePer condition: cannot fetch sync from <nil> (1:12)
 7 failed to execute oncePer condition: cannot slice 0 (1:24)
 7 failed to execute oncePer condition: invalid operation: string + <nil> (1:24)
```

The trigger still fired for these apps once the status was complete (84-85 "TRIGGERED" log lines).

**Evidence, `[0:15]` bucket change, nil-safe config (v3.5.6):**

```
2026-11-06T18:00:37.265Z reconciledAt=2026-11-06T18:00:36Z bucket[0:15]=2026-11-06T18:0
2026-11-06T18:11:12.188Z reconciledAt=2026-11-06T18:11:11Z bucket[0:15]=2026-11-06T18:1
distinct non-null [0:15] buckets: 2
```

v3.3.18: events at 18:03:10 (bucket `18:0`) and 18:11:22 (bucket `18:1`). The second event comes at the first reconcile after the bucket starts, so the gap is 10 min plus up to one reconcile interval.

A wrong expression is dangerous. In an earlier attempt of this spike I wrote `(app.status.reconciledAt ?? )[0:15]` by mistake. The controller then logged this for every app and sent nothing for any app:

```
Failed to get api: errors getting apis: [unexpected token Bracket(")") (1:29) ...]
```

I also tried the recipient `webhook:babis` once. The controller logged `notification service 'webhook' is not supported`. The draft recipient `babis` is correct.

### 2b. Real heartbeat rate with `[0:16]` (one bucket per minute)

**Result:** the event rate follows the ArgoCD reconcile rate, not the bucket. A reconcile takes place every 122-260 s, so each app sends one event per reconcile. This is 16-28 events per app per hour.

**Evidence (v3.5.6, 15 min window 18:11:41 to 18:26:41, change events in the window: 0):**

```
bad-path       events=4 gaps(s)=[257, 227, 211] mean=232s
guestbook-1    events=4 gaps(s)=[260, 138, 250] mean=216s
guestbook-2    events=4 gaps(s)=[223, 141, 231] mean=199s
late-app       events=5 gaps(s)=[140, 147, 122, 238] mean=162s
```

An earlier full run (same script, 15 min window) gave 7, 7, 6 and 6 events per app, gaps 128-260 s, mean 148-174 s. So the interval varies by run (mean 150-230 s).

v3.3.18 (6 min window):

```
bad-path       events=2 gaps(s)=[151]
guestbook-1    events=2 gaps(s)=[126]
guestbook-2    events=2 gaps(s)=[196]
late-app       events=2 gaps(s)=[152]
```

**Impact:** `[0:15]` (10 min) is the realistic choice: about 6 events per app per hour. For 300 apps this is 1800 requests per hour per edge. For 1000 edges this is about 500 requests per second at the hub. The coalescing in the draft handles status writes. The hub also needs a cheap, fast HTTP path.

## Item 3. Global subscriptions

**Result:** works. No annotation on any Application. Events arrive for all apps, also for the bad-path app.

**Evidence (v3.5.6, requests per app and trigger, first minutes):**

```
subscribe annotations on the Applications: []
  bad-path       change 2   heartbeat 2
  fin-app        change 4   heartbeat 2
  guestbook-1    change 4   heartbeat 2
  guestbook-2    change 4   heartbeat 2
  guestbook-3    change 5   heartbeat 2
```

**Impact:** none. Use `recipients: [babis]`, where `babis` is the name in `service.webhook.babis`.

## Item 4. `toJson` on missing status fields

**Result:** valid JSON. Missing fields become `null`. The template does not fail. The controller does not skip the send.

**Evidence, the very first event of a just-created app (guestbook-1):**

```
{"edge": "spike-edge", "event": "state", "app": {"name": "guestbook-1", "sync": null, "health": null, "revision": null, "reconciledAt": null}}
{"edge": "spike-edge", "event": "state", "app": {"name": "guestbook-1", "sync": "OutOfSync", "health": "Missing", "revision": "8088f4c0...", "reconciledAt": "2026-11-06T16:26:03Z"}}
```

A new app sends 6 requests in the first seconds (late-app: 4 `change` and 2 `heartbeat`). The states are null/null, OutOfSync/Missing, OutOfSync/Healthy, Synced/Progressing, Synced/Healthy.

**Evidence, bad-path app (`path: does-not-exist`):**

```
{"app": {"name": "bad-path", "sync": null, "health": null, "revision": null, "reconciledAt": null}}
{"app": {"name": "bad-path", "sync": "Unknown", "health": "Healthy", "revision": "HEAD", "reconciledAt": "2026-11-06T16:26:05Z"}}
```

Surprises: health is `Healthy` for an app that cannot render. The revision is the string `HEAD` (the `targetRevision`), not a commit SHA. The app has a `ComparisonError` condition that the template does not show.

**Proof of validity:** `analyze.py bodies` parses every captured body with python `json`. Result on v3.5.6: 40 of 40 valid. On v3.3.18: 37 of 37 valid.

**Impact:** the hub must accept `null` for `sync`, `health` and `revision`. The hub must treat `sync=Unknown` as "has a problem". Do not trust `health` alone.

## Item 5. Deletes

**Result:** with the finalizer, a `deleted` event arrives at once. Without the finalizer, nothing arrives.

**Evidence, fin-app (finalizer `resources-finalizer.argocd.argoproj.io`), v3.5.6:**

```
2026-11-06T16:31:15.445Z deleted {"edge": "spike-edge", "event": "deleted", "app": {"name": "fin-app", "sync": "Synced", "health": "Healthy", ...}}
2026-11-06T16:31:15.581Z change  {"edge": "spike-edge", "event": "deleted", "app": {"name": "fin-app", "sync": "OutOfSync", "health": "Progressing", ...}}
```

On v3.3.18 only the first line (the `deleted` trigger) arrived. On v3.5.6 the `change` trigger also sent a body with `"event": "deleted"` (the body test uses `deletionTimestamp`).

**Evidence, guestbook-3 (no finalizer), both versions:** no request for the app in the 60 s after the delete. The list of requests since the delete command is empty.

**Impact:** stale prune is the main delete path, as the draft fallback says. The hub must accept more than one `deleted` body per app.

## Item 6. Edge cost

**Result:** yes. The notifications controller writes the annotation `notified.notifications.argoproj.io` on the Application for each send. This is one extra write on the Application per send, next to the reconcile write of the application controller.

**Evidence (v3.5.6, guestbook-1, 15 min window, `kubectl get -w` stream with managedFields):**

```
watch objects: 9, resourceVersion changes: 8
  changes where the notified annotation changed: 4
  changes where only status.reconciledAt changed: 4
  managedFields entries that moved, per manager: {'argocd-application-controller': 4, 'argocd-notifications-controller': 4}
heartbeat events sent for guestbook-1 in the window: 4
```

So 4 events gave 4 notification writes and 4 reconcile writes. The reconcile writes happen without Babis. v3.3.18 (6 min): 4 changes, 2 events, 2 and 2.

The annotation value is a JSON map. Its keys contain the `oncePer` value, so each new value adds one key:

```
"2026-11-06T17:4:babis-heartbeat:[0].X_5TO4MPCKAyY0ipFgr6_IraRNs:babis:": 1793987258
"2026-11-06T17:5:babis-heartbeat:[0].X_5TO4MPCKAyY0ipFgr6_IraRNs:babis:": 1793987458
```

Size: guestbook-1 had 14 keys, 1259 bytes after about 1 h. late-app had 19 keys, 1764 bytes.

**Impact:** open: I saw no pruning of old keys in 1 h. If the keys are never pruned, then at about 90 bytes per key and 6 keys per hour (`[0:15]`) the annotation reaches the 256 KiB limit for all annotations of one object in about 19 days. At `[0:16]` it takes much less. I did not run longer. Test this before the spec depends on `oncePer` for a long time.

## Item 7. Hub down

**Result:** 2 min outage with a slow failure (Service with no endpoints): the event arrives late. A fast failure (DNS): the controller gives up after 4 attempts and the change event is lost. The heartbeat repairs the state later.

### 7 (slow failure, logger scaled to 0 for 2 min)

The connect call hangs until the TCP timeout (about 133 s). The controller blocks during this time. Other apps are not processed in this time.

**Evidence (v3.5.6):**

```
logger down at 18:31:25; guestbook-1 Deployment scaled to 3
logger back at 18:33:26
{"msg":"Sending notification about condition 'babis-change...' to '{babis }'","resource":"argocd/guestbook-1","time":"18:31:26Z"}
18:33:39 [ERR] POST http://hub-logger.babis-spike.svc:8080/...?trigger=change request failed: ... dial tcp 10.96.149.108:8080: connect: connection timed out
18:33:39 [DEBUG] POST ...?trigger=change: retrying in 1s (3 left)
18:33:40.613Z change {... "app": {"name": "guestbook-1", "sync": "OutOfSync", "health": "Progressing", ...}}
```

The event arrived 14 s after the hub came back, 2 min 14 s after the change. There is no request in the logger between 18:31:25 and 18:33:26. Between the two lines there is no controller log line for any other app. All other apps sent at 18:33:40. On v3.3.18 the same happened (hang from 18:22:33 to 18:24:47, events at 18:24:48).

### 7b (fast failure, hub URL with a name that does not resolve, 90 s)

**Evidence (v3.5.6):**

```
18:33:52 [ERR] POST http://nonexistent-hub...?trigger=change request failed: ... lookup nonexistent-hub.babis-spike.svc ...
18:33:52 [DEBUG] ... retrying in 1s (3 left)
18:33:57 ... retrying in 2s (2 left)
18:34:00 ... retrying in 4s (1 left)
Failed to notify recipient {babis } defined in resource argocd/late-app: POST ...?trigger=change giving up after 4 attempt(s)
18:34:19 Sending notification ... babis-change ... (late-app)   -> failed again
18:34:36 Notification about condition 'babis-change...' already sent to '{babis }'   (late-app; hub still down until 18:35:19)
```

After the hub came back (18:35:19) the logger received only one event for late-app: a `heartbeat` at 18:36:50 with `OutOfSync/Healthy`. The `change` events for this change never arrived. The final count of `change` requests for late-app did not grow (v3.5.6: 4 before and after; v3.3.18: 3 before and after). The log line "already sent" shows that the controller marked the condition as sent although no send worked.

**Impact:** the draft says a restart loses at most 30 s and the next heartbeat repairs it. This still holds for the heartbeat, but change events are not reliable. The hub must treat events as hints and the heartbeat as the source of truth. A hub outage also blocks the whole notifications controller on the edge (up to about 2 min per send) but does not affect ArgoCD itself.

## Item 8. Notifications controller in the core install

**Result:** present in `manifests/core-install.yaml` in both versions (the Deployment `argocd-notifications-controller`).

**Evidence:**

```
v3.5.6:  32759:          app.kubernetes.io/name: argocd-notifications-controller
v3.3.18: 32228:          app.kubernetes.io/name: argocd-notifications-controller
```

**Impact:** none. The `$babis-token` and the config still need edit rights on the two objects.

## Surprises

1. **A revisited state does not fire again.** The change trigger uses `oncePer` with `sync + health + revision`. The annotation keeps every value it has sent. When guestbook-2 went `OutOfSync/Healthy` and back to `Synced/Healthy` (a value sent before), no change event was sent. Only a heartbeat carried the new state. Evidence (v3.5.6, since the scale back at 18:28:22):

   ```
   18:28:23.900Z heartbeat {... "name": "guestbook-2", "sync": "Synced", "health": "Healthy", ...}
   18:30:44.394Z heartbeat {... "name": "guestbook-2", "sync": "Synced", "health": "Healthy", ...}
   ```

   There are no `change` lines for the revert. This means: the heartbeat period is the delay for any return to an old state (up to 10 min plus one reconcile with `[0:15]`). I did not test an `oncePer` that uses a value that always grows (for example `app.status.health.lastTransitionTime` or `app.status.operationState.finishedAt`). It is a candidate fix, not proven.
2. **Duplicates.** One state change can send a `change` and a `heartbeat` request at once (v3.5.6 outage recovery: 2 requests 3 ms apart for the same state). A new app sends 6 requests. One scale-up of a Deployment sent 2 `change` requests (v3.5.6: OutOfSync/Progressing, then OutOfSync/Healthy) and 0 `heartbeat`. Requests per change: one per distinct (sync, health, revision) value, plus one `heartbeat` when the bucket changes at that moment.
3. **`sync.revision` is `HEAD` for an app that cannot render** and health is `Healthy`. See item 4.
4. **A bad expression stops all notifications** for all apps (see item 2a).
5. **The first event of an app has no status** (all `null`), and the second one comes within seconds.

## Measured numbers

| Measure | v3.5.6 | v3.3.18 |
|---------|--------|---------|
| `[0:15]` events for guestbook-1 | 18:00:37, 18:11:12 (10.6 min apart) | 18:03:10, 18:11:22 (8.2 min, bucket change) |
| `[0:16]` window | 15 min | 6 min |
| Heartbeat gap per app (mean, this run) | 162-232 s | 126-196 s |
| Heartbeat gap range (all runs) | 122-260 s | 126-196 s |
| Heartbeat events per app per hour | 16-20 (this run), 24-28 (earlier run) | not measured (6 min window, 2 events per app) |
| Heartbeat events per app per hour with `[0:15]` | about 6 | about 6 |
| Requests for a new app (late-app) | 6 (4 change + 2 heartbeat) | not counted separately (late-app total before the outage tests: 3 change) |
| Requests for one Deployment scale-up | 2 change | 1 change (the heartbeat 90 s later is routine) |
| Requests for a delete, app with finalizer | 2 (`deleted` + `change` with `deleted` body) | 1 |
| Requests for a delete, app without finalizer | 0 | 0 |
| Application writes per send (annotation) | 1 | 1 |
| Annotation size after about 1 h | 14 keys, 1259 bytes | 12 keys, 1089 bytes |
| Hub down 2 min (slow failure): delay of the event | 2 min 14 s (arrives after the hub is back) | 2 min 15 s |
| Hub down (fast failure, DNS, 90 s) | change event lost; heartbeat 91 s after the URL was restored | change event lost; heartbeat 128 s after the URL was restored |
| Content-Type | `application/json` | `application/json` |

## Corrected config

This config works on both versions. Changes against the draft:

- `oncePer` expressions are nil-safe (`?.` and `??`).
- One template is shown. The spike used three templates (same body) only to tell the triggers apart.
- The `$babis-token` header, the global `subscriptions` and `toJson` stay as in the draft.
- Heartbeat bucket `[0:15]`.

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
trigger.babis-change: |
  - when: "true"
    oncePer: (app.status.sync?.status ?? "") + (app.status.health?.status ?? "") + (app.status.sync?.revision ?? "")
    send: [babis-app]
trigger.babis-heartbeat: |
  - when: "true"
    oncePer: (app.status.reconciledAt ?? "")[0:15]    # 10 min bucket
    send: [babis-app]
trigger.babis-deleted: |
  - when: app.metadata.deletionTimestamp != nil
    oncePer: app.metadata.deletionTimestamp
    send: [babis-app]
subscriptions: |
  - recipients: [babis]               # the service name, not webhook:babis
    triggers: [babis-change, babis-heartbeat, babis-deleted]
```

Notes on the config:

- In the template, `{{toJson .app.status.sync.status}}` worked on empty status in the spike. No nil-safe form is needed there.
- The spike body also had `"reconciledAt": {{toJson .app.status.reconciledAt}}`. It is useful for the hub (age of the data) and valid with `null`.
- Item 6 (annotation growth) and the revisit problem (surprise 1) are open design points for the spec.
