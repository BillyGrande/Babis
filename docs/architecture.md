# Architecture

How edge-deployer works, why it is shaped this way, and where the
boundaries are. See [positioning.md](positioning.md) for scope rationale.

Legend: sections marked **[implemented]** exist today; **[planned]** are
agreed design not yet built.

## Topology

```
 EDGE (each site)                            CENTRAL CLUSTER
┌───────────────────────────────┐           ┌──────────────────────────────┐
│  ArgoCD (autonomous)          │           │  edge-deployer operator      │
│   ▲ installs/converges        │           │   ├─ ingest HTTP :8090 ◄─────┼── POST /api/v1alpha1/reports
│  edgedeploy CLI               │  HTTPS,   │   └─ Edge reconciler         │
│   ├─ bootstrap                │  outbound │        (Reachable condition) │
│   ├─ apply                    │  only     │                              │
│   └─ report  ── cron/timer ───┼──────────►│  Edge CRs = fleet inventory  │
│                               │           │   `kubectl get edges`        │
│  [air-gap] local OCI registry │           └──────────────────────────────┘
│  (e.g. Hauler) serving images │
│  and the ArgoCD chart         │
└───────────────────────────────┘
```

Three invariants define the topology:

1. **Edges only dial out.** The operator never connects to an edge; there
   is nothing on an edge to connect *to*.
2. **Each edge is autonomous.** Its own ArgoCD pulls desired state from
   Git. edge-deployer never carries manifests between clusters.
3. **The edge-side footprint is a one-shot CLI**, not a daemon. Anything
   long-lived runs centrally.

## Components

### `edgedeploy` CLI (edge side) [implemented]

A single static Go binary. It shells out to whatever `kubectl` and `helm`
are on PATH (overridable via `--kubectl`/`--helm`) rather than embedding
client-go/Helm SDK: small binary, respects the user's kubeconfig, works
with any Kubernetes distribution. All subcommands support `--dry-run`
(mutating commands are printed, state inspection still runs).

| Command | Role |
|---|---|
| `bootstrap` | Install/converge ArgoCD via `helm upgrade --install` (idempotent). Chart source: upstream argo-helm repo, a local `.tgz`, or `oci://` on a local air-gap registry (`--plain-http`). |
| `apply` | Collect, render, apply manifests. Plain `.yaml` as-is; `.yaml.tmpl` are Go templates rendered with `--values` (strict: `missingkey=error`). Rendered output pipes to `kubectl apply -f -`, never to disk. |
| `status` | Compact sync/health/revision of local ArgoCD Applications; `--json` for machines. |
| `report` | One-shot heartbeat to the operator. Designed for cron/systemd timers. |

### Central operator [implemented]

A controller-runtime manager with two members:

- **Ingest server** (`internal/operator/ingest`): `POST
  /api/v1alpha1/reports`, bearer-token auth, 1 MiB body cap. First report
  from an unknown edge **auto-registers** it (creates the Edge object);
  every report rewrites the Edge's status subresource. No manual fleet
  enrollment step.
- **Edge reconciler** (`internal/operator/controller`): owns exactly one
  property — the `Reachable` condition. The decision core
  (`Reachability(lastReport, timeout, now)`) is a pure function; the
  reconciler requeues itself precisely at the moment the answer could
  change (the heartbeat deadline). A fresh report triggers reconcile
  through the normal watch, flipping the edge back to reachable with no
  polling.

Reachability is **inferred, never probed**: silence past the timeout
(default 15 m, per-edge override `spec.heartbeatTimeout`) means
unreachable. This is the only definition compatible with outbound-only.

### Edge CRD (`fleet.edgedeployer.io/v1alpha1`) [implemented]

- `spec` — operator-side intent: `displayName`, `heartbeatTimeout`,
  `expectedStoreVersion`.
- `status` — edge-reported observation: `lastReportTime`, `labels`,
  per-app `apps[]` plus `appsTotal/appsSynced/appsHealthy` roll-ups,
  `storeVersion`, `registryHealthy`, and `conditions` (reconciler-owned).

The split matters: ingest writes status only, humans/automation write spec
only, and drift questions ("is the edge on the expected store version?")
are a comparison between the two. CRDs — not a database — are the state
store: they get RBAC, watch semantics, `kubectl` UX, and GitOps-ability
for free. Historical/analytical storage is an optional *additive* consumer
(see Roadmap), never a replacement.

### Wire contract (`internal/wire`) [implemented]

The report JSON is the compatibility boundary between fleets of old CLIs
and a new operator. Rules: additive changes only after v0.1; edges never
import k8s API machinery (the `wire.App` ↔ `v1alpha1.AppSummary`
duplication is deliberate); the edge name must be a DNS-1123 subdomain
because it becomes an object name.

## Day-0 flows

### Online edge

```bash
edgedeploy bootstrap --chart-version <v> -f values.yaml
edgedeploy apply ./bootstrap-apps --values edge-values.yaml
# + cron: edgedeploy report --hub https://fleet.example.com ...
```

### Air-gapped edge [partially planned]

Today: bring your own local registry (e.g. a Hauler store serving on
`localhost:5000`), then

```bash
edgedeploy bootstrap --chart oci://localhost:5000/charts/argo-cd --plain-http
edgedeploy apply ./bootstrap-apps --values edge-values.yaml
edgedeploy report ... --registry-url http://localhost:5000 --store-version <v>
```

Planned: `edgedeploy airgap` subcommands wrapping store load / registry
serve / health, and automatic store-version detection so the report flags
disappear.

## Edge identity and the in-cluster secret [planned]

The unifying idea: **an edge's identity is a set of labels, declared once,
consumed twice.**

ArgoCD ApplicationSets running *on the edge* can select work using the
cluster generator, which matches labels on cluster secrets. For an
autonomous install that means a declarative secret for the local cluster:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: in-cluster
  namespace: argocd
  labels:
    argocd.argoproj.io/secret-type: cluster
    region: gr-north          # user-defined identity labels
    tier: retail
stringData:
  name: in-cluster
  server: https://kubernetes.default.svc
```

Design decision: the tool manages this object **structurally, not via
templating**. The secret has a tiny, fixed schema; only the labels vary.
Making every user maintain a `.yaml.tmpl` of it would be boilerplate with
sharp edges (typos in `secret-type`, wrong server URL, lost labels on
re-render). Instead:

```bash
edgedeploy bootstrap --cluster-label region=gr-north --cluster-label tier=retail
# and for day-2 identity changes without a redeploy:
edgedeploy cluster-labels set key=value [...]
```

Semantics: create the secret if absent; otherwise **merge** the given
labels, preserving unknown ones (so different actors can own different
labels). The same labels flow into `report` automatically, so local
ApplicationSet selection and the central fleet inventory index on one
identity source.

Templating (`apply --values`) remains the mechanism for what is genuinely
user-shaped: the bootstrap Applications themselves — repo URLs, path
segments per environment, target revisions. The rule of thumb:
**known-schema objects get structured commands; user-shaped manifests get
templates.** Organization-specific parameters must arrive as labels or
template values, never as named CLI flags.

The structured path is a convenience default, not a restriction: users who
need extra fields on the cluster secret (annotations, additional data keys)
can always template the whole secret themselves and `apply` it — the
`--cluster-label` merge semantics must tolerate a secret it didn't create.

## Security model

- Ingest auth is a single shared bearer token
  (`EDGEDEPLOY_INGEST_TOKEN` / `EDGEDEPLOY_TOKEN`), constant-time compared.
  Acknowledged v1 limitation; per-edge tokens or mTLS are on the roadmap.
  TLS termination is deferred to the user's ingress in front of :8090.
- Reports are treated as untrusted input: size-capped, JSON-decoded,
  validated (DNS-1123 edge name) before any API call.
- A report can only influence the Edge object bearing its name; spec is
  never writable through ingest.
- The operator's RBAC (generated, `config/rbac/`) is scoped to `edges` and
  `edges/status`.

## Failure modes and behavior

| Failure | Behavior |
|---|---|
| WAN down at report time | The one-shot fails; cron retries on the next tick. Missed heartbeats ≤ timeout are invisible; longer flips Reachable=False, which self-heals on the next delivered report. |
| Operator down | Edges keep reconciling their own workloads (autonomy invariant). Reports fail until it returns; no edge-side queueing in v1. |
| Duplicate edge names | Two edges write one Edge object — last writer wins per field. Prevented operationally (names from serial numbers/hostnames); per-edge auth will make it enforceable. |
| Clock skew on an edge | Harmless: `lastReportTime` is stamped by the operator at receipt, not taken from the report. |

## Repository layout

```
cmd/edgedeploy/        CLI entry (signal-aware context)
cmd/operator/          manager wiring: reconciler + ingest runnable
api/v1alpha1/          Edge types; deepcopy + CRDs generated (make generate manifests)
internal/cli/          cobra constructors only — no business logic, no globals
internal/argocd/       helm install + Application state via kubectl
internal/manifests/    collect / render / apply
internal/execx/        exec wrapper (--dry-run / --verbose)
internal/wire/         report contract (shared, k8s-free)
internal/operator/     controller/ (reconciler), ingest/ (HTTP → Edge status)
config/crd|rbac/       generated — never hand-edit
docs/                  this file, positioning.md
```

## Roadmap (ordered)

1. **v0.1 hardening** — docs, kind-based e2e, release binaries, CI images.
2. **Cluster identity** — `--cluster-label` / `cluster-labels` as above.
3. **Air-gap first-class** — `airgap` subcommands (Hauler-backed), store
   version detection.
4. **Operator deploy assets** — Helm chart/kustomize, container image,
   ingress/TLS guidance.
5. **Per-edge auth** — token-per-edge or mTLS, enrollment flow.
6. **History consumer** — optional Postgres-backed report archive for
   trend questions; additive, CRDs remain source of truth for *now*.
7. **argocd-agent interop** — represent agent-managed clusters as Edge
   objects so hybrid fleets get one inventory.
