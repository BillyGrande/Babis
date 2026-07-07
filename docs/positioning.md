# Positioning

What edge-deployer is, what it deliberately is not, and how to evaluate
feature ideas against that boundary. This document exists so the scope
survives feature temptations.

## The one-sentence pitch

> edge-deployer gets ArgoCD onto edges that start disconnected, and tells
> you what state your fleet is in — regardless of whether each edge is
> autonomous or argocd-agent-managed.

## The niche

Most multi-cluster GitOps tooling assumes the hub can reach every cluster's
API server, or at least that clusters can pull from the internet on day 0.
Edge fleets are usually the opposite: many small clusters behind NAT, flaky
WAN links, sometimes fully air-gapped. Two problems in that topology remain
unsolved by the mainstream tools:

1. **Day 0 on a disconnected box.** Before any GitOps engine can work,
   something must get artifacts onto the machine, stand up a local OCI
   registry, install ArgoCD from it, and stamp the edge's identity.
   Every distribution tool assumes this already happened.
2. **Fleet-wide state, outbound-only.** Once hundreds of autonomous ArgoCD
   installs are running, "which edges are out of sync / degraded / silent?"
   has no answer that doesn't require inbound connectivity to the edges.

edge-deployer answers exactly these two, and nothing else.

## Relationship to argocd-agent

[argocd-agent](https://github.com/argoproj-labs/argocd-agent) is the
ecosystem's answer to hub-spoke ArgoCD over outbound-only connections, and
it will keep absorbing the connected and hybrid use cases. edge-deployer
does not compete with it — it is orthogonal:

- **argocd-agent assumes ArgoCD is already installed** on the workload
  cluster. edge-deployer's bootstrap is useful to argocd-agent users on
  day one, literally.
- **argocd-agent's principal reports status for clusters it manages.**
  edge-deployer's inventory matters for the autonomous and air-gapped edges
  the agent doesn't cover — and in hybrid fleets, the Edge CRD can be the
  single aggregation point across both kinds.

If a proposed feature would make edge-deployer an *alternative* to
argocd-agent rather than a complement, the right move is to contribute that
feature to argocd-agent instead.

## Relationship to Fleet, Sveltos, OCM/ACM

Rancher Fleet, Sveltos, and Open Cluster Management distribute manifests to
many clusters. edge-deployer does not distribute anything between clusters:
each edge pulls its own desired state via its own ArgoCD from Git. If you
need centralized manifest distribution, use one of those — they compose
fine with edge-deployer (they deliver the Applications; edge-deployer did
day 0 and reports state).

## Non-goals (hard lines)

- **No manifest-distribution protocol.** Never. This is the trap that turns
  "complementary" into "competitor" overnight, and it is the feature class
  with the worst failure modes (a buggy applier can prune live workloads
  fleet-wide). ArgoCD is the sync engine; edge-deployer never syncs.
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
