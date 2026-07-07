# Positioning

What Babis is, what it deliberately is not, and how to evaluate
feature ideas against that boundary. This document exists so the scope
survives feature temptations.

## The one-sentence pitch

> Babis is day-0 and fleet observability for edge clusters running
> their own ArgoCD: install it anywhere — online or air-gapped — stamp the
> edge's identity, and know what state the whole fleet is in, outbound-only.

## The niche

Most multi-cluster GitOps tooling assumes the hub can reach every cluster's
API server. Edge fleets are usually the opposite: many small clusters
behind NAT, flaky WAN links, sometimes fully air-gapped. Two problems in
that topology remain unsolved by the mainstream tools:

1. **Day 0, repeatably, on any edge.** Before any GitOps engine can work,
   something must install ArgoCD, stamp the edge's identity, and apply the
   seed Applications — the same way on the hundredth edge as on the first.
   On disconnected edges (the hardest case, and first-class here) that also
   means doing it from a local OCI registry instead of the internet. Every
   distribution tool assumes all of this already happened.
2. **Fleet-wide state, outbound-only.** Once hundreds of autonomous ArgoCD
   installs are running, "which edges are out of sync / degraded / silent?"
   has no answer that doesn't require inbound connectivity to the edges.

Babis answers exactly these two, and nothing else. Air-gap support
is a capability, not the definition: connected edges get the same
one-command bootstrap and the same inventory.

## Relationship to argocd-agent

[argocd-agent](https://github.com/argoproj-labs/argocd-agent) is the
ecosystem's answer to hub-spoke ArgoCD over outbound-only connections, and
it will keep absorbing the connected and hybrid use cases. babis
does not compete with it — it is orthogonal:

- **argocd-agent assumes ArgoCD is already installed** on the workload
  cluster. babis's bootstrap is useful to argocd-agent users on
  day one, literally.
- **argocd-agent's principal reports status for clusters it manages.**
  babis's inventory matters for the autonomous and air-gapped edges
  the agent doesn't cover — and in hybrid fleets, the Edge CRD can be the
  single aggregation point across both kinds.

If a proposed feature would make Babis an *alternative* to
argocd-agent rather than a complement, the right move is to contribute that
feature to argocd-agent instead.

## Relationship to Zarf and Hauler

[Zarf](https://zarf.dev) and [Hauler](https://hauler.dev) solve artifact
packaging and transport into disconnected environments — Zarf as a full
declarative package (images, charts, git repos, in-cluster registry via
`zarf init`), Hauler as an OCI artifact store served as a local registry.
Babis builds **on top of** whichever is present and competes with
neither: `bootstrap` only needs *some* OCI registry reachable from the
edge, and does not care whether Zarf, Hauler, or anything else put it
there. Packaging, transport, and registry seeding are permanently out of
scope — the air-gap roadmap means *integrating with* these tools
(store/registry health in reports, store version detection), never
replacing them.

The gap Babis targets that neither covers: they are per-cluster
and deploy-time — no fleet inventory, no heartbeats, no ArgoCD awareness
(identity stamping, Application sync/health). And for the common
*semi-connected* edge (outbound-only WAN, Git pulled remotely, only images
local), a lightweight local registry plus babis's reporting is a
better fit than a full air-gap package.

## Relationship to Fleet, Sveltos, OCM/ACM

Rancher Fleet, Sveltos, and Open Cluster Management distribute manifests to
many clusters. Babis does not distribute anything between clusters:
each edge pulls its own desired state via its own ArgoCD from Git. If you
need centralized manifest distribution, use one of those — they compose
fine with babis (they deliver the Applications; babis did
day 0 and reports state).

## Non-goals (hard lines)

- **No manifest-distribution protocol.** Never. This is the trap that turns
  "complementary" into "competitor" overnight, and it is the feature class
  with the worst failure modes (a buggy applier can prune live workloads
  fleet-wide). ArgoCD is the sync engine; babis never syncs.
- **No inbound connections to edges.** Any feature that requires the
  central side to reach an edge is out of scope, however convenient.
- **No resident edge agent.** The edge-side footprint is a CLI invoked by
  an operator or a timer. If a feature seems to need a daemon on the edge,
  redesign it as a one-shot or move it to the central operator.
- **No opinionated GitOps repo layout.** The tool must not care how users
  structure environments, app-of-apps, or ApplicationSets. Parameterization
  happens through user-owned templates and cluster secret labels, not
  tool-defined conventions.

## Litmus tests for new features

Ask, in order:

1. Does it require the hub to dial an edge? → **reject.**
2. Does it move/sync manifests between clusters? → **reject** (or upstream
   it to argocd-agent/Fleet).
3. Does it encode one organization's naming or repo layout? → **generalize
   first** (labels/values, not named flags).
4. Does it help day-0 bootstrap or fleet observability? → in scope.
5. Would argocd-agent users benefit without adopting anything else? →
   strong signal it belongs here.
