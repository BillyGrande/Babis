# Intent: Babis as a hub for autonomous ArgoCD edges

- **Author:** BillyGrande (draft by Claude)
- **Status:** Draft — awaiting review
- **Date:** 2026-10-30
- **Supersedes:** [0001-edge-identity](../0001-edge-identity/intent.md) (folded in)
- **Spec:** [spec.md](spec.md)

## Problem

The prototype is read-only. Central can see the fleet but cannot change
anything on it. To upgrade ArgoCD or change an edge's identity, someone
must reach each edge by hand (SSH, local console) — the exact thing an
outbound-only fleet makes hard.

Identity is also typed twice: once in the in-cluster cluster secret for
local ApplicationSets, once as `report --label` flags for the central
inventory (see 0001). The two drift silently.

The prototype code is small and unreleased. A redesign costs little now
and much more after the first release freezes the wire format.

## Proposed outcome

Babis is a hub for a fleet of autonomous ArgoCD installs, built in small
phases that each ship alone:

1. **Telemetry first, for existing edges.** Edges that already run ArgoCD
   (installed by any method) and can dial out report the health of their
   ArgoCD installation and their Applications to central. Babis is useful
   before it installs anything.
2. **Bootstrap** any edge — online or air-gapped — with ArgoCD and its
   identity labels in one command. Works with no hub at all.
3. **Small central desired state.** Per edge: the ArgoCD chart version and
   the identity labels. Edges fetch it and converge. Each edge decides
   which fields it accepts.
4. **An extension point** for later concerns (Hauler stores, with
   hauler-helper) that does not change the core.

Done looks like:

- An existing edge, with ArgoCD installed by Helm or plain manifests,
  appears in `kubectl get edges` with its ArgoCD version, component
  readiness and Application sync/health — without reinstalling anything.
- An air-gapped edge with no hub runs `babis bootstrap` from a local
  registry with `--cluster-label region=gr-north`. ArgoCD is installed, and
  an ApplicationSet cluster generator selecting `region: gr-north` matches.
- Central changes `spec.argocd.chartVersion` on an Edge. An edge that
  accepts that field upgrades on its next check-in, and the Edge shows
  `Converged=True`. An edge that does not accept it reports a rejection,
  and nothing changes on it.

## Affected users and systems

- **Fleet operators** who manage the central cluster and Edge objects.
- **Edge provisioning** (scripts, cloud-init, Ansible) that runs
  `bootstrap`.
- **Platform teams** who write ApplicationSets that run on edges.
- **hauler-helper** (future project) as the first extension.

## Constraints

- **Outbound-only.** Central never opens a connection to an edge.
- **No resident agent.** Babis uses no memory on an edge between runs.
  Low-RAM edges are a target.
- **No application manifests.** Central manages the edge's ArgoCD
  installation and identity — never the applications ArgoCD deploys. Git
  stays their source.
- **Edge veto.** By default an edge accepts nothing from central. It opts
  in per field.
- **Per-edge auth first.** No desired-state feature ships on a shared
  token.
- **Central runs anywhere.** Any Kubernetes cluster, cloud or on-prem.
- **Air-gapped edges:** bootstrap only. Telemetry is not required.
- **Small steps.** Each phase is useful alone.

## Out of scope (for now)

- Telemetry from fully air-gapped edges (relays, physical media).
- Fleet-wide rollout objects (select edges by label, staged upgrades).
- Imperative one-off commands ("refresh now").
- Hauler-specific features in the core.

## Open questions

1. ~~Language~~ — resolved: Rust edge, Go central
   ([spec](spec.md#language-decision)).
2. **Smallest target edge:** RAM and architecture?
3. **hauler-helper:** does "push to remote" push into an edge registry, or
   into a relay registry that edges pull from? Which language?
4. **Positioning:** exact wording of the new boundary ("manages the
   ArgoCD installation, never the applications").
