#!/usr/bin/env bash
# P1b phase B step 1: prove the combined `babis-state` trigger from spec.md.
# Copies the helpers it needs from run.sh on purpose (no shared lib.sh).
# Needs kind, kubectl, curl, docker, jq, python3. Own KUBECONFIG per version.
# Usage: bash combined.sh   (env: ARGOCD_VERSIONS, KEEP=1, OUT=dir, STEADY_MIN)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARGOCD_VERSIONS="${ARGOCD_VERSIONS:-v3.5.6 v3.3.18}"
KEEP="${KEEP:-0}"
STEADY_MIN="${STEADY_MIN:-20}"
OUT="${OUT:-$(mktemp -d)}"
TOKEN="spike-$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
mkdir -p "$OUT"
echo "evidence directory: $OUT"

CLUSTERS=()
PIDS=()
cleanup() {
  local rc=$?
  trap - EXIT
  if [ ${#PIDS[@]} -gt 0 ]; then kill "${PIDS[@]}" 2>/dev/null || true; fi
  if [ "$KEEP" != "1" ]; then
    for c in "${CLUSTERS[@]}"; do kind delete cluster --name "$c" >/dev/null 2>&1 || true; done
  else
    echo "KEEP=1: clusters kept: ${CLUSTERS[*]}"
  fi
  exit $rc
}
trap cleanup EXIT

log() { echo "[$(date +%H:%M:%S)] $*"; }
ev() { echo "$@" | tee -a "$VD/evidence.txt"; }
section() { ev; ev "=== $* ==="; }
fail() { echo "FAIL: $*" | tee -a "$VD/evidence.txt" >&2; exit 1; }
k() { kubectl --context "kind-$CLUSTER" "$@"; }
an() { python3 -I "$HERE/analyze.py" "$VD/parts" "$@"; }
now() { date -u +%Y-%m-%dT%H:%M:%S; }

wait_for() { # DESC TIMEOUT CMD...
  local desc=$1 timeout=$2; shift 2
  local deadline=$((SECONDS + timeout))
  until "$@"; do
    [ $SECONDS -lt "$deadline" ] || fail "timeout (${timeout}s) waiting for: $desc"
    sleep 5
  done
  log "ok: $desc"
}

# Soft assert: records the result, the run goes on, the version fails at the end.
FAILS=0
check() { # DESC CMD...
  local desc=$1; shift
  if "$@"; then ev "ASSERT ok: $desc"; else ev "ASSERT FAIL: $desc"; FAILS=$((FAILS + 1)); fi
}

grab() {
  local p
  for p in $(k -n babis-spike get pods -l app=hub-logger -o name 2>/dev/null); do
    k -n babis-spike logs "$p" >"$VD/parts/${p#pod/}.jsonl" 2>/dev/null || true
  done
}

preload_image() {
  local image=$1 node="$CLUSTER-control-plane" i
  for i in 1 2 3 4 5; do
    docker exec "$node" crictl pull "$image" >/dev/null && return 0
    log "pull $image failed (try $i), retry in $((i * 10))s"
    sleep $((i * 10))
  done
  local mirror="docker.io/library/${image##*/docker/library/}"
  [ "$mirror" != "docker.io/library/$image" ] || fail "cannot pull $image"
  docker exec "$node" ctr -n k8s.io images pull "$mirror" >/dev/null &&
    docker exec "$node" ctr -n k8s.io images tag "$mirror" "$image" >/dev/null ||
    fail "cannot pull $image or $mirror"
}

# The config of spec.md "Config", as it is. Differences (instrumentation only):
# the URL, the "?trigger=" query in the path, a second template for the deleted
# trigger (same body), and "reconciledAt" in the body.
apply_config() {
  cat >"$VD/notifications-cm.yaml" <<'CM'
data:
  service.webhook.babis: |
    url: http://hub-logger.babis-spike.svc:8080
    headers:
    - name: Authorization
      value: Bearer $babis-token
    - name: Content-Type
      value: application/json
  template.babis-app: |
    webhook:
      babis:
        method: POST
        path: /api/v1alpha1/app-events?trigger=state
        body: |
          {"edge": "shop-athens-042",
           "event": "{{if .app.metadata.deletionTimestamp}}deleted{{else}}state{{end}}",
           "app": {"name": {{toJson .app.metadata.name}},
                   "sync": {{toJson .app.status.sync.status}},
                   "health": {{toJson .app.status.health.status}},
                   "revision": {{toJson .app.status.sync.revision}},
                   "reconciledAt": {{toJson .app.status.reconciledAt}}}}
  template.babis-deleted: |
    webhook:
      babis:
        method: POST
        path: /api/v1alpha1/app-events?trigger=deleted
        body: |
          {"edge": "shop-athens-042",
           "event": "{{if .app.metadata.deletionTimestamp}}deleted{{else}}state{{end}}",
           "app": {"name": {{toJson .app.metadata.name}},
                   "sync": {{toJson .app.status.sync.status}},
                   "health": {{toJson .app.status.health.status}},
                   "revision": {{toJson .app.status.sync.revision}},
                   "reconciledAt": {{toJson .app.status.reconciledAt}}}}
  trigger.babis-state: |
    - when: "true"
      # The state plus a 10-minute bucket of reconciledAt. A new state sends
      # at once; every app sends its state once per bucket.
      oncePer: >-
        (app.status.sync?.status ?? "") + "/" +
        (app.status.health?.status ?? "") + "/" +
        (app.status.sync?.revision ?? "") + "/" +
        (app.status.reconciledAt ?? "")[0:15]
      send: [babis-app]
  trigger.babis-deleted: |
    - when: app.metadata.deletionTimestamp != nil
      oncePer: app.metadata.deletionTimestamp
      send: [babis-deleted]
  subscriptions: |
    - recipients: [babis]
      triggers: [babis-state, babis-deleted]
CM
  k -n argocd patch configmap argocd-notifications-cm --type=merge --patch-file "$VD/notifications-cm.yaml" >/dev/null
  k -n argocd rollout restart deploy/argocd-notifications-controller >/dev/null
  k -n argocd rollout status deploy/argocd-notifications-controller --timeout=180s >/dev/null
}

app_manifest() { # NAME PATH
  cat <<APP
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: $1
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps
    targetRevision: HEAD
    path: $2
  destination:
    server: https://kubernetes.default.svc
    namespace: ns-$1
  syncPolicy:
    automated: {}
    syncOptions: [CreateNamespace=true]
APP
}

app_state() { k -n argocd get app "$1" -o 'jsonpath={.status.sync.status}/{.status.health.status}' 2>/dev/null || true; }
app_ok() { [ "$(app_state "$1")" = "Synced/Healthy" ]; }
apps_ok() { app_ok guestbook-1 && app_ok guestbook-2; }
all_have_events() { grab; local a; for a in guestbook-1 guestbook-2; do [ -n "$(an statefirst "$a" Synced Healthy "")" ] || return 1; done; }
got_state() { grab; [ -n "$(an statefirst "$1" "$2" "$3" "$4")" ]; } # APP SYNC HEALTH SINCE
secs() { date -u -d "$(echo "$1" | sed 's/Z$//; s/T/ /')" +%s; }      # UTC timestamp -> epoch

run_version() {
  local VER=$1 start=$SECONDS
  local CLUSTER="babis-combined-${VER#v}"
  CLUSTER="${CLUSTER//./-}"
  local VD="$OUT/$VER"
  mkdir -p "$VD/parts"
  : >"$VD/evidence.txt"
  export KUBECONFIG="$VD/kubeconfig"
  log "=== ArgoCD $VER (cluster $CLUSTER) ==="
  ev "date: $(date -u +%Y-%m-%dT%H:%M:%SZ); ArgoCD $VER; steady window ${STEADY_MIN} min"

  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  kind create cluster --name "$CLUSTER" --wait 120s
  local manifest="$VD/install.yaml" redis
  curl -fsSL --retry 3 -o "$manifest" \
    "https://raw.githubusercontent.com/argoproj/argo-cd/${VER}/manifests/install.yaml"
  redis="$(grep -oE 'image: [^ ]*/redis:[^ ]*' "$manifest" | sed -n '1,1p' | cut -d' ' -f2)"
  [ -n "$redis" ] || fail "no redis image in install.yaml"
  preload_image "$redis"
  docker exec "$CLUSTER-control-plane" crictl pull docker.io/library/python:3-alpine >/dev/null || true
  k create namespace argocd >/dev/null
  k apply -n argocd --server-side --force-conflicts -f "$manifest" >/dev/null
  k -n argocd patch deploy argocd-redis --type=json -p '[
    {"op":"test","path":"/spec/template/spec/containers/0/name","value":"redis"},
    {"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"}]' >/dev/null
  k -n argocd rollout status deploy --timeout=600s >/dev/null || fail "ArgoCD deployments not ready"
  k -n argocd rollout status statefulset --timeout=600s >/dev/null || fail "ArgoCD statefulset not ready"
  log "ArgoCD $VER ready after $((SECONDS - start))s"

  k create namespace babis-spike >/dev/null
  k -n babis-spike create configmap hub-logger --from-file=logger.py="$HERE/logger.py" >/dev/null
  k apply -f - >/dev/null <<'LOGGER'
apiVersion: apps/v1
kind: Deployment
metadata: {name: hub-logger, namespace: babis-spike}
spec:
  replicas: 1
  selector: {matchLabels: {app: hub-logger}}
  template:
    metadata: {labels: {app: hub-logger}}
    spec:
      containers:
      - name: logger
        image: python:3-alpine
        imagePullPolicy: IfNotPresent
        command: [python, -u, /app/logger.py]
        ports: [{containerPort: 8080}]
        readinessProbe: {tcpSocket: {port: 8080}}
        volumeMounts: [{name: app, mountPath: /app}]
      volumes: [{name: app, configMap: {name: hub-logger}}]
---
apiVersion: v1
kind: Service
metadata: {name: hub-logger, namespace: babis-spike}
spec:
  selector: {app: hub-logger}
  ports: [{port: 8080, targetPort: 8080}]
LOGGER
  k -n babis-spike rollout status deploy/hub-logger --timeout=300s >/dev/null || fail "logger not ready"

  k -n argocd patch secret argocd-notifications-secret --type=merge \
    -p "{\"stringData\":{\"babis-token\":\"$TOKEN\"}}" >/dev/null
  apply_config
  local T_CFG T_CFG_EPOCH
  T_CFG="$(now)"
  T_CFG_EPOCH=$(date -u +%s)
  ev "spec config applied at $T_CFG UTC"

  local a
  for a in guestbook-1 guestbook-2; do app_manifest "$a" guestbook | k apply -f - >/dev/null; done
  app_manifest bad-path does-not-exist | k apply -f - >/dev/null
  wait_for "guestbook apps Synced/Healthy" 420 apps_ok
  wait_for "Synced/Healthy request for both guestbook apps" 300 all_have_events
  sleep 90 # let the first reconciles and the first bucket settle
  grab

  section "c. Steady window of ${STEADY_MIN} min, no changes"
  local T_WIN
  T_WIN="$(now)"
  ev "window starts $T_WIN UTC (app state: guestbook-1=$(app_state guestbook-1) guestbook-2=$(app_state guestbook-2))"
  sleep $((STEADY_MIN * 60))
  grab
  ev "window ends $(now) UTC. State-trigger requests per app per 10 min bucket of reconciledAt:"
  an buckets "$T_WIN" | tee -a "$VD/evidence.txt" || true
  check "c. at most 1 request per app per bucket in the steady window" an buckets "$T_WIN"

  section "a. New state is sent at once (guestbook-1 scaled to 2 replicas)"
  local T_A
  T_A="$(now)"
  k -n ns-guestbook-1 scale deploy/guestbook-ui --replicas=2 >/dev/null
  local d=$((SECONDS + 60))
  until got_state guestbook-1 OutOfSync Healthy "$T_A" || got_state guestbook-1 OutOfSync Progressing "$T_A"; do
    [ $SECONDS -lt "$d" ] || break
    sleep 2
  done
  grab
  ev "scale at $T_A UTC. Requests for guestbook-1 since then:"
  an lines guestbook-1 "$T_A" 10 | tee -a "$VD/evidence.txt"
  local ta
  ta="$(an lines guestbook-1 "$T_A" 1 | cut -d' ' -f1)"
  if [ -n "$ta" ]; then ev "delay of the first request: $(($(secs "$ta") - $(secs "$T_A"))) s (whole seconds)"; fi
  check "a. a request with the new state (OutOfSync) arrives within 60 s" \
    test -n "$(an statefirst guestbook-1 OutOfSync Healthy "$T_A")$(an statefirst guestbook-1 OutOfSync Progressing "$T_A")"
  ev "app state now: $(app_state guestbook-1)"
  sleep 30 # let the app settle (OutOfSync/Healthy) before the revisit

  section "b. Revisited state is sent at the next bucket (guestbook-1 scaled back to 1)"
  local rec bucket_end deadline_epoch T_B
  rec="$(k -n argocd get app guestbook-1 -o jsonpath='{.status.reconciledAt}')"
  # Bucket = reconciledAt[0:15] = 10 min. Next bucket start = bucket + 10 min.
  bucket_end=$(($(secs "${rec:0:15}0:00Z") + 600))
  deadline_epoch=$((bucket_end + 300))
  T_B="$(now)"
  ev "reconciledAt before the scale back: $rec"
  ev "next bucket starts $(date -u -d "@$bucket_end" +%H:%M:%S); deadline (next bucket + 5 min): $(date -u -d "@$deadline_epoch" +%H:%M:%S)"
  ev "state before: $(app_state guestbook-1). Requests of guestbook-1 so far: $(an count guestbook-1 state "")"
  k -n ns-guestbook-1 scale deploy/guestbook-ui --replicas=1 >/dev/null
  until got_state guestbook-1 Synced Healthy "$T_B"; do
    [ "$(date -u +%s)" -lt "$deadline_epoch" ] || break
    sleep 5
  done
  grab
  ev "scale back at $T_B UTC. Requests for guestbook-1 since then:"
  an lines guestbook-1 "$T_B" 10 | tee -a "$VD/evidence.txt"
  local tb
  tb="$(an statefirst guestbook-1 Synced Healthy "$T_B")"
  if [ -n "$tb" ]; then
    ev "delay until Synced/Healthy request: $(($(secs "$tb") - $(secs "$T_B"))) s after the scale back; $(($(secs "$tb") - bucket_end)) s after the next bucket start (negative = before it)"
  else
    ev "no Synced/Healthy request arrived before the deadline"
  fi
  check "b. Synced/Healthy request arrives before next bucket + 5 min" test -n "$tb"

  section "d. oncePer errors in the notifications controller log"
  k -n argocd logs deploy/argocd-notifications-controller --tail=-1 >"$VD/notif-final.log" 2>&1 || true
  local cut_iso before after
  cut_iso="$(date -u -d "@$((T_CFG_EPOCH + 60))" +%Y-%m-%dT%H:%M:%S)"
  before="$(jq -R 'fromjson? | select((.msg // "") | contains("failed to execute oncePer")) | select(.time[0:19] < "'"$cut_iso"'")' "$VD/notif-final.log" | jq -s length)"
  after="$(jq -R 'fromjson? | select((.msg // "") | contains("failed to execute oncePer")) | select(.time[0:19] >= "'"$cut_iso"'")' "$VD/notif-final.log" | jq -s length)"
  ev "lines 'failed to execute oncePer': before config+60 s ($cut_iso): $before; after: $after"
  ev "all lines containing 'failed to execute oncePer': $(grep -c 'failed to execute oncePer' "$VD/notif-final.log" || true)"
  check "d. no oncePer errors after the first 60 s" test "$after" -eq 0

  section "Totals"
  ev "requests per app and trigger:"
  an summary | tee -a "$VD/evidence.txt"
  for a in guestbook-1 guestbook-2 bad-path; do
    ev "$a: total requests=$(an count "$a" state "")  $(k -n argocd get app "$a" -o json | an annot)"
  done
  ev "bad-path app state: $(app_state bad-path)"
  an bodies | tee -a "$VD/evidence.txt"
  section "Done ArgoCD $VER in $((SECONDS - start))s, failed assertions: $FAILS"
  echo "$((SECONDS - start))" >"$VD/seconds"

  if [ "$KEEP" != "1" ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
  [ "$FAILS" -eq 0 ]
}

for v in $ARGOCD_VERSIONS; do
  c="babis-combined-${v#v}"
  CLUSTERS+=("${c//./-}")
  mkdir -p "$OUT/$v"
  ( run_version "$v" ) >"$OUT/$v.log" 2>&1 &
  PIDS+=($!)
done

rc=0
i=0
for v in $ARGOCD_VERSIONS; do
  if wait "${PIDS[$i]}"; then
    echo "##### $v: PASS in $(cat "$OUT/$v/seconds")s"
  else
    echo "##### $v: FAIL (last 20 log lines of $OUT/$v.log)"
    tail -20 "$OUT/$v.log"
    rc=1
  fi
  cat "$OUT/$v/evidence.txt"
  i=$((i + 1))
done
PIDS=()
[ $rc -eq 0 ] && echo "PASS: all versions ($ARGOCD_VERSIONS); evidence in $OUT"
exit $rc
