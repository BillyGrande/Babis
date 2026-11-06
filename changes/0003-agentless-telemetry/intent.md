# Intent: agentless telemetry from ArgoCD notifications

- **Author:** BillyGrande (draft by Claude)
- **Status:** Draft 2026-11-06 — spike in progress
- **Date:** 2026-11-06
- **Roadmap:** [P1b - Agentless telemetry](../../docs/roadmap.md#p1b---agentless-telemetry)
- **Draft design:** [0002 spec § P1b](../0002-redesign/spec.md#p1b--agentless-telemetry-argocd-notifications)
- **Spec:** [spec.md](spec.md) (after the spike)

## Problem

P1 (`v0.1.0`) needs a CronJob, a ServiceAccount, a read-only Role and an
image pull on each edge. Some edges do not allow this. Examples: a cluster
that another team owns, a strict admission policy, or no registry access
for a new image. These edges do not appear in the fleet at all today.

On many of these edges, the operator can still edit the ArgoCD
notification config (`argocd-notifications-cm` and
`argocd-notifications-secret`). ArgoCD already runs its notifications
controller there.

## Proposed outcome

An edge where nothing new can be installed appears as an `Edge` in
`kubectl get edges`. Its apps (name, sync, health, revision) and
`Reachable` come from ArgoCD's own notifications controller, which posts
events to central. Babis adds no process, no RBAC and no RAM on the edge.

The check-in (P1) stays the full path. Notifications are the fallback: no
ArgoCD version, no component readiness (`ArgoCDReady=Unknown`), no
identity labels from the cluster secret.

## Users

- **Edge admins** with edit rights on the two ArgoCD notification objects
  only. They apply one kustomize patch (`deploy/edge-notifications/`).
- **Fleet operators** who read `kubectl get edges`. They see the same
  `Edge` objects for both kinds of edges, with `status.telemetry.source`
  that tells them which kind.

## Constraints

- ArgoCD-only. Use ArgoCD's notifications engine as it is; no fork, no
  custom service.
- Outbound-only. Central never calls the edge.
- Zero Babis process on the edge.
- Wire changes are additive. `v0.1.0` edges keep working against the new
  operator.
- Central scale: 1000 edges with 300 apps each must not overload the API
  server. Events must be coalesced before they become status writes.
- The operator runs 2 replicas, and every replica accepts ingest.

## Open questions

1. The four items the 0002 spec marks "to verify": the `$babis-token`
   reference in a header value, string slicing in `oncePer`, global
   `subscriptions` without annotations, and `toJson` on missing status
   fields. The spike answers them.
2. Heartbeat rate: how often does each Application send an event with no
   change? This sets the default `heartbeatTimeout` for these edges and the
   request rate at central.
3. Deletes: does a `deleted` event arrive for an app without the resources
   finalizer? If not, stale prune is the main delete path.
4. Coalescing across 2 replicas: `status.apps` is an atomic list, so two
   replicas that flush their own buffers can remove each other's apps.
5. Cost on the edge: the notifications controller writes an annotation on
   the Application for each send.
6. Is the notifications controller in the ArgoCD core install?
