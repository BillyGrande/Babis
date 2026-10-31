#!/usr/bin/env bash
# End-to-end test on kind: one cluster per ArgoCD version.
# Usage: make e2e   (or: ARGOCD_VERSIONS="v3.5.6" KEEP=1 bash test/e2e/run.sh)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

# Pinned on 2026-10-30. Upstream supports the 3 latest minors (3.5, 3.4, 3.3);
# we test the newest and the oldest, each at its latest patch release
# (https://api.github.com/repos/argoproj/argo-cd/releases).
ARGOCD_VERSIONS="${ARGOCD_VERSIONS:-v3.5.6 v3.3.18}"
KEEP="${KEEP:-0}"
NS_FLEET=babis-fleet
EDGE=e2e-edge
HUB=http://babis-operator.babis-system.svc:8090

CLUSTER=""
TMP="$(mktemp -d)"
cleanup() {
  local rc=$?
  rm -rf "$TMP"
  if [ -n "$CLUSTER" ] && [ "$KEEP" != "1" ]; then
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  elif [ -n "$CLUSTER" ]; then
    echo "KEEP=1: cluster $CLUSTER kept (context kind-$CLUSTER)"
  fi
  exit $rc
}
trap cleanup EXIT

log() { echo "[$(date +%H:%M:%S)] $*"; }

fail() {
  echo "FAIL: $*" >&2
  echo "--- kubectl get edge -n $NS_FLEET -o yaml" >&2
  kubectl get edge -n "$NS_FLEET" -o yaml >&2 || true
  echo "--- last checkin job logs" >&2
  local pod
  pod="$(kubectl -n argocd get pods -l job-name --sort-by=.metadata.creationTimestamp \
    -o name 2>/dev/null | grep babis-checkin | tail -1 || true)"
  if [ -n "$pod" ]; then kubectl -n argocd logs "$pod" >&2 || true; else echo "(no job pod)" >&2; fi
  echo "--- operator logs" >&2
  kubectl -n babis-system logs deploy/babis-operator --tail=30 >&2 || true
  exit 1
}

# edge_jsonpath EXPR -> prints value of the Edge field.
edge_jp() { kubectl get edge "$EDGE" -n "$NS_FLEET" -o "jsonpath=$1" 2>/dev/null || true; }
cond() { edge_jp "{.status.conditions[?(@.type==\"$1\")].status}"; }

# wait_for DESC TIMEOUT_SECONDS COMMAND...  (command succeeds when satisfied)
wait_for() {
  local desc=$1 timeout=$2; shift 2
  local deadline=$((SECONDS + timeout))
  until "$@"; do
    [ $SECONDS -lt $deadline ] || fail "timeout (${timeout}s) waiting for: $desc"
    sleep 5
  done
  log "ok: $desc"
}

cond_is() { [ "$(cond "$1")" = "$2" ]; }
ready_state() { [ "$(cond ArgoCDReady)" = "True" ] && [ "$(cond Reachable)" = "True" ]; }

first_ok() {
  [ "$(edge_jp '{.status.argocd.version}' | sed 's/^v//')" = "${VERSION#v}" ] &&
    [ "$(edge_jp '{.status.appsTotal}')" = "3" ] && ready_state
}

# ArgoCD pulls redis from public.ecr.aws with imagePullPolicy Always. Anonymous
# pulls from shared CI runner IPs get "429 Too Many Requests" there. So pull it
# into the kind node with retries (then the same image from Docker Hub, tagged
# with the original name) and set the pull policy to IfNotPresent in the test
# cluster only.
preload_image() {
  local image=$1 node="$CLUSTER-control-plane" i
  for i in 1 2 3 4 5; do
    docker exec "$node" crictl pull "$image" >/dev/null && return 0
    log "pull $image failed (try $i), retry in $((i * 10))s"
    sleep $((i * 10))
  done
  local mirror="docker.io/library/${image##*/docker/library/}"
  [ "$mirror" != "docker.io/library/$image" ] || fail "cannot pull $image"
  log "pull $mirror instead"
  docker exec "$node" ctr -n k8s.io images pull "$mirror" >/dev/null &&
    docker exec "$node" ctr -n k8s.io images tag "$mirror" "$image" >/dev/null ||
    fail "cannot pull $image or $mirror"
}

run_version() {
  VERSION=$1
  CLUSTER="babis-e2e-${VERSION#v}"
  CLUSTER="${CLUSTER//./-}"
  log "=== ArgoCD $VERSION (cluster $CLUSTER) ==="
  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  kind create cluster --name "$CLUSTER" --wait 120s
  kind load docker-image babis-edge:dev babis-operator:dev --name "$CLUSTER"

  local manifest="$TMP/install-$VERSION.yaml" redis
  curl -fsSL --retry 3 -o "$manifest" \
    "https://raw.githubusercontent.com/argoproj/argo-cd/${VERSION}/manifests/install.yaml"
  redis="$(grep -oE 'image: [^ ]*/redis:[^ ]*' "$manifest" | head -1 | cut -d' ' -f2)"
  [ -n "$redis" ] || fail "no redis image in ArgoCD $VERSION install.yaml"
  preload_image "$redis"

  kubectl create namespace argocd
  kubectl apply -n argocd --server-side --force-conflicts -f "$manifest" >/dev/null
  kubectl -n argocd patch deploy argocd-redis --type=json -p '[
    {"op":"test","path":"/spec/template/spec/containers/0/name","value":"redis"},
    {"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"}]'
  # Image pulls on shared CI runners are sometimes slow; on timeout, show why.
  if ! kubectl -n argocd rollout status deploy --timeout=600s ||
    ! kubectl -n argocd rollout status statefulset --timeout=600s; then
    echo "FAIL: ArgoCD $VERSION did not become ready" >&2
    kubectl -n argocd get pods -o wide >&2 || true
    { kubectl -n argocd get events --sort-by=.lastTimestamp | tail -30; } >&2 || true
    exit 1
  fi

  for n in 1 2 3; do
    kubectl apply -f - <<APP
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: guestbook-$n
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps
    targetRevision: HEAD
    path: guestbook
  destination:
    server: https://kubernetes.default.svc
    namespace: default
APP
  done

  local token
  token="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  kubectl apply -f deploy/central/namespaces.yaml
  kubectl -n babis-system create secret generic babis-ingest-token --from-literal=token="$token"
  kubectl -n argocd create secret generic babis-token --from-literal=token="$token"

  kubectl apply -k deploy/central
  kubectl -n babis-system patch deploy babis-operator --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--heartbeat-timeout=2m"}]'
  kubectl -n babis-system rollout status deploy/babis-operator --timeout=180s

  kubectl apply -k deploy/edge
  kubectl -n argocd patch configmap babis-edge --type=merge \
    -p "{\"data\":{\"hub\":\"$HUB\",\"edgeName\":\"$EDGE\"}}"
  kubectl -n argocd patch cronjob babis-checkin --type=merge -p '{"spec":{"schedule":"* * * * *"}}'
  kubectl -n argocd patch cronjob babis-checkin --type=json \
    -p '[{"op":"test","path":"/spec/jobTemplate/spec/template/spec/containers/0/args/5","value":"--jitter"},{"op":"replace","path":"/spec/jobTemplate/spec/template/spec/containers/0/args/6","value":"5s"}]'

  # 1. Edge appears and is healthy.
  wait_for "edge $EDGE: version $VERSION, appsTotal=3, ArgoCDReady=True, Reachable=True" 240 first_ok

  # 2. ArgoCD degraded.
  kubectl -n argocd scale deploy/argocd-repo-server --replicas=0
  wait_for "ArgoCDReady=False with repo-server at 0" 240 cond_is ArgoCDReady False
  kubectl -n argocd scale deploy/argocd-repo-server --replicas=1
  wait_for "ArgoCDReady=True after repo-server restored" 300 cond_is ArgoCDReady True

  # 3. Heartbeat loss and recovery.
  kubectl -n argocd patch cronjob babis-checkin --type=merge -p '{"spec":{"suspend":true}}'
  wait_for "Reachable=False after suspending the CronJob" 240 cond_is Reachable False
  kubectl -n argocd patch cronjob babis-checkin --type=merge -p '{"spec":{"suspend":false}}'
  wait_for "Reachable=True after resuming the CronJob" 180 cond_is Reachable True

  # 4. Edge RBAC is read-only.
  local sa=system:serviceaccount:argocd:babis-checkin res verb out
  for res in applications.argoproj.io deployments statefulsets secrets; do
    for verb in create update patch delete; do
      out="$(kubectl auth can-i "$verb" "$res" -n argocd --as="$sa" 2>&1 || true)"
      [ "$out" = "no" ] || fail "RBAC: $sa can $verb $res (got: $out)"
    done
  done
  out="$(kubectl auth can-i get secrets/argocd-secret -n argocd --as="$sa" 2>&1 || true)"
  [ "$out" = "no" ] || fail "RBAC: $sa can get secret argocd-secret (got: $out)"
  out="$(kubectl auth can-i get secrets/in-cluster -n argocd --as="$sa" 2>&1 || true)"
  [ "$out" = "yes" ] || fail "RBAC: $sa cannot get secret in-cluster (got: $out)"
  log "ok: edge RBAC has no write verbs and reads only secret in-cluster"

  # 5. Printer columns.
  local table
  table="$(kubectl get edges -n "$NS_FLEET")"
  echo "$table"
  for col in ARGOCD READY REACHABLE; do
    echo "$table" | head -1 | grep -qw "$col" || fail "printer column $col missing"
  done
  log "ok: printer columns"

  if [ "$KEEP" != "1" ]; then
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
    CLUSTER=""
  fi
}

START=$SECONDS
make images
for v in $ARGOCD_VERSIONS; do
  run_version "$v"
done
log "PASS: all versions ($ARGOCD_VERSIONS) in $((SECONDS - START))s"
