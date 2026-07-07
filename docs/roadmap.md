# Roadmap

The single source of truth for where Babis is going. Milestones are
thematic, ordered, and intentionally small — each one must be independently
useful and shippable. Feature ideas are evaluated against the litmus tests
in [positioning.md](positioning.md) before they land here.

## v0.1 — Babis shows up *(current)*

The minimum honest product: day-0 on one edge, visibility over many.

- [x] `babis bootstrap` — install/converge ArgoCD via Helm (upstream repo,
  local `.tgz`, or `oci://` on a local registry)
- [x] `babis apply` — strict-templated manifest apply (`.yaml.tmpl` +
  `--values`)
- [x] `babis status` / `babis report` — local state, cron-friendly
  heartbeat
- [x] Operator: ingest endpoint, Edge CRD auto-registration, `Reachable`
  condition from heartbeat freshness
- [ ] kind-based e2e test (operator + CLI against a real cluster)
- [ ] Release automation: versioned static binaries (linux amd64/arm64),
  checksums
- [ ] Operator container image + minimal deploy manifest
- [ ] First public release under github.com/BillyGrande/babis

## v0.2 — Babis learns names (edge identity)

An edge's identity is a set of labels, declared once, consumed twice:
locally by ArgoCD ApplicationSets (cluster generator), centrally by the
fleet inventory.

- [ ] `bootstrap --cluster-label k=v` — structured management of the
  declarative in-cluster secret (create-or-merge, never clobber labels it
  doesn't own; must tolerate user-templated secrets)
- [ ] `babis cluster-labels set/list` — day-2 identity changes without a
  redeploy
- [ ] Labels flow into `report` automatically (single identity source)

## v0.3 — Babis works offline (air-gap, first-class)

Integrate with local artifact stores — own none of them
([positioning](positioning.md#relationship-to-zarf-and-hauler)).

- [ ] Store/registry detection: Hauler stores, Zarf-seeded registries,
  plain OCI registries
- [ ] Automatic store-version detection (retire the `--store-version` flag)
- [ ] Registry health in every report without extra flags
- [ ] Documented end-to-end air-gap walkthroughs (Hauler path, Zarf path)

## v0.4 — Babis locks the door (operations & auth)

What production adoption needs before anyone should trust it.

- [ ] Helm chart (or kustomize overlay) for the operator; TLS/ingress
  guidance for the ingest endpoint
- [ ] Per-edge auth: token-per-edge or mTLS; enrollment flow that fits
  fleet provisioning (name collisions become enforceable, not just
  conventional)
- [ ] Prometheus metrics from the operator (fleet roll-ups: unreachable
  count, degraded apps by label)

## v0.5 — Babis remembers (history)

Current state lives in CRDs, always. History is an additive consumer.

- [ ] Optional report archive (Postgres) answering trend questions:
  "when did this edge last drift", "flap frequency by region"
- [ ] Retention and schema documented as non-authoritative (CRDs remain
  the source of truth for *now*)

## v0.6 — Babis gets along (hybrid fleets)

- [ ] Represent argocd-agent-managed clusters as Edge objects so hybrid
  fleets (some autonomous, some agent-managed) get one inventory
- [ ] Evaluate: report enrichment from agent principal status

## Beyond / undecided

Parked ideas that pass the litmus tests but have no milestone yet:

- Edge-side report queueing for long WAN outages (v1 behavior: cron
  retries are enough)
- `kubectl` plugin (`kubectl babis ...`) for the central side
- Multi-tenancy: Edge namespaces per team / RBAC guidance

## Explicit non-goals

Never on this roadmap, per [positioning.md](positioning.md): manifest
distribution between clusters, inbound connections to edges, resident edge
agents, artifact packaging/transport, opinionated GitOps repo layouts.
