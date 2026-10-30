# Roadmap

The single source of truth for where Babis is going. Phases are ordered
and small; each one ships alone and is useful alone. The design is in
[changes/0002-redesign/spec.md](../changes/0002-redesign/spec.md); the P1
build record is in [plan.md](../changes/0002-redesign/plan.md). Feature
ideas are checked against the litmus tests in
[positioning.md](positioning.md) before they land here.

## P1 - Telemetry *(current: done except release)*

Read-only `babis checkin` on edges that already run ArgoCD, and a central
operator.

- [x] `babis checkin` (Rust, read-only, from a CronJob, no Kubernetes
  client library)
- [x] Wire contract: JSON Schemas and golden fixtures in `wire/`, tested by
  Go and Rust
- [x] Edge CRD `fleet.babis.dev/v1alpha1` with `Reachable` and
  `ArgoCDReady` conditions
- [x] Operator: ingest `POST /api/v1alpha1/checkin`, auto-registration,
  server-side apply
- [x] Operator image and kustomize manifests (`deploy/central`,
  `deploy/edge`)
- [x] kind e2e on the oldest and the newest supported ArgoCD
  (`make e2e`)
- [x] CI: generated-file drift, edge size budget, contract tests, e2e
- [x] Load test and scale hardening (`make load`; 1000 edges, jitter,
  `--max-edges`, `--max-inflight`)
- [x] Docs and a kind quickstart in the README
- [ ] Release automation: versioned static binaries (linux amd64/arm64),
  checksums, published images
- [ ] First public release under github.com/BillyGrande/babis
- [ ] HTTPS against a real hub (ingress with TLS) tested end to end
- [x] Document the `--max-edges` overshoot (the count comes from the
  informer cache and can lag a few creates)

## P1b - Agentless telemetry

For edges where nothing can be installed. ArgoCD's own notifications
controller posts Application events to central. Zero Babis footprint on the
edge. Starts with a spike on kind that checks the four open items in the
spec.

- [ ] Spike: confirm the notification template on a real ArgoCD
- [ ] Wire: `app-event` schema and fixtures
- [ ] Operator: `POST /api/v1alpha1/app-events`, per-edge coalescing,
  stale-app prune, `status.telemetry.source`
- [ ] `deploy/edge-notifications/` kustomize patch
- [ ] e2e scenario without the CronJob

## P2 - Bootstrap

Day-0 install of ArgoCD, with identity.

- [ ] `babis bootstrap`: ArgoCD via Helm (shell-out to `helm`)
- [ ] `--cluster-label k=v`: identity labels on the cluster secret
  `in-cluster` (create or merge; never remove labels Babis did not set;
  works on a user-templated secret)
- [ ] Edge config in ConfigMap `babis-edge` (`hub`, `edgeName`, chart
  source)
- [ ] Air-gap bootstrap from a local OCI registry (`--chart oci://...
  --plain-http`), with a local registry such as Hauler or Zarf
- [ ] Kubeconfig support for host cron or systemd timers
- [ ] Optional `--hub` also installs the check-in CronJob

## P3 - Desired state

Gate: per-edge auth lands first.

- [ ] Per-edge tokens (admin creates the Edge; operator creates the token
  Secret; edge name comes from the token); auto-registration off. Optionally
  mTLS.
- [ ] `spec.argocd.chartVersion` and `spec.labels` in the check-in response
- [ ] Edge accept list (default: none); per-field applied, rejected, failed
- [ ] ArgoCD upgrade with `helm upgrade` and rollback
- [ ] `Converged` condition
- [ ] Update `positioning.md` (scope extends to the ArgoCD installation
  lifecycle)
- [ ] Helm chart or kustomize overlay for the operator, TLS guidance

## P4 - Extensions

Gate: P3 stable.

- [ ] `extensions` in the wire format
- [ ] Edge plugins `babis-<name>` on PATH
- [ ] First extension: Hauler (own CRD and controller, store version and
  registry health)

## Later / undecided

Ideas that pass the litmus tests but have no phase yet.

- Prometheus metrics from the operator (fleet roll-ups: unreachable count,
  degraded apps by label)
- History: an optional archive (for example Postgres) as an additive
  consumer; CRDs stay the source of truth for now
- Hybrid fleets with argocd-agent: show agent-managed clusters as Edge
  objects, one inventory for both kinds
- `kubectl` plugin (`kubectl babis ...`) for the central side
- Heartbeat/status split for fleets far above 5000 edges (see spec
  § Beyond 5000); waits for a load run that shows the need
- Edge-side queueing for long WAN outages (cron retries are enough now)
- Multi-tenancy: Edge namespaces per team, RBAC guidance
- Fleet rollout objects, imperative commands, air-gap telemetry relays

## Explicit non-goals

Never on this roadmap, per [positioning.md](positioning.md): manifest
distribution between clusters, inbound connections to edges, resident edge
agents, artifact packaging/transport, opinionated GitOps repo layouts.
