# Intent: edge identity from one source

- **Author:** BillyGrande (draft by Claude)
- **Status:** Draft — awaiting review
- **Date:** 2026-10-30
- **Roadmap:** [v0.2 — Babis learns names](../../docs/roadmap.md#v02--babis-learns-names-edge-identity)
- **Design notes:** [architecture.md § Edge identity](../../docs/architecture.md#edge-identity-and-the-in-cluster-secret-planned)

## Problem

An edge's identity lives in two places today. Both are maintained by hand
and nothing connects them.

1. **Locally**, ArgoCD ApplicationSets on the edge select work with the
   cluster generator, which matches labels on the `in-cluster` cluster
   secret. Babis does not manage that secret. Users write it themselves,
   usually as a `.yaml.tmpl`. A typo in `argocd.argoproj.io/secret-type` or
   the server URL makes generator selection fail silently, and a re-render
   can drop labels that another actor added.
2. **Centrally**, `babis report --label k=v` sends labels to the operator.
   These flags are typed into the cron line as a separate list.

The two lists can drift. An edge can select `tier=retail` work locally
while the fleet inventory says `tier=lab`, and nothing signals the
mismatch. Every fleet question asked by label is then wrong.

Smaller defect in the same area: `report --label` accepts `foo` (no `=`) as
key `foo` with an empty value, and does not validate keys or values against
Kubernetes label syntax.

## Proposed outcome

An edge's identity is a set of labels, **declared once on the edge,
consumed twice**: by the local ArgoCD and by the central inventory.

- `babis bootstrap --cluster-label k=v` creates the in-cluster secret if it
  is absent, or merges the labels into it if it exists.
- `babis cluster-labels set` / `list` changes or shows identity on day 2,
  without re-running bootstrap.
- `babis report` reads the labels from the secret and sends them. The cron
  line no longer carries a separate label list.

Done looks like:

1. After `bootstrap --cluster-label region=gr-north`, an ApplicationSet with
   a cluster generator selecting `region: gr-north` matches the local
   cluster.
2. The next `report` shows `region=gr-north` on that Edge in the central
   inventory, with no label flags on the cron line.
3. After `cluster-labels set region=gr-south`, both the local selection and
   the inventory change, with no other action.

## Affected users and systems

- **Fleet provisioning** (scripts, cloud-init, Ansible) that runs
  `bootstrap` on new edges.
- **Platform teams** that write ApplicationSets running on edges.
- **Fleet owners** who query the central inventory.
- **Code:** `bootstrap`, a new `cluster-labels` command, `report`,
  `internal/argocd` (read/write of the cluster secret). Possibly
  `internal/operator/ingest` (see open question 6). The wire format should
  not change: `labels` already exists in `wire.Report`.

## Constraints

- **Positioning** ([litmus tests](../../docs/positioning.md#litmus-tests-for-new-features)):
  serves day-0 and fleet observability; no hub-to-edge connection; no
  manifest distribution. Label keys are user-defined — no named
  organization flags such as `--region`.
- **Never clobber.** Merge only. Preserve labels, annotations and data keys
  that babis did not set.
- **Tolerate foreign secrets.** The merge must work on a secret the user
  templated and applied with `babis apply`.
- **Shell out on the edge.** Use the `kubectl` on PATH; no client-go in the
  CLI.
- **`--dry-run`** prints every mutation and performs none.
- **Strict input.** Invalid label keys or values fail with a clear error.
  They are never silently dropped or coerced.
- **Wire compatibility.** Old CLIs that still send `--label` keep working
  against new operators.
- **Air-gap.** No network access beyond the local API server.
- **No resident agent.** Identity changes are one-shot commands.

## Open questions

1. **Precedence.** If `report --label` is given and the secret also has
   labels: merge (flag wins), error, or deprecate `--label`?
2. **Filtering.** Does `report` send every label on the secret, or skip
   system labels such as `argocd.argoproj.io/secret-type` (by prefix, or by
   an allow-list)?
3. **Ownership.** Does babis track which labels it manages (for example, an
   annotation listing managed keys), or is it merge-only with explicit
   removal?
4. **Removal.** Is `cluster-labels unset` in scope for v0.2? The roadmap
   lists only `set` and `list`.
5. **Which secret.** Always `in-cluster` in the ArgoCD namespace, or
   discover the cluster secret whose server is
   `https://kubernetes.default.svc`? What if a user-templated secret has
   another name?
6. **Inventory selectors.** Should the operator mirror reported labels into
   Edge `metadata.labels`, so `kubectl get edges -l region=gr-north` works?
   Today they land in `status.labels`, which label selectors cannot query.
   Mirroring crosses the "ingest writes status only" rule in
   architecture.md.
