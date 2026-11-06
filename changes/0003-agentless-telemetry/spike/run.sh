#!/usr/bin/env bash
# P1b spike: do ArgoCD notifications work as agentless telemetry?
# Kept for reproduction. Not wired into make or CI. Needs kind, kubectl, curl,
# docker, jq, python3. Uses its own KUBECONFIG per version (never your contexts).
# Usage: bash run.sh   (env: ARGOCD_VERSIONS, KEEP=1, OUT=dir, HB_MIN_NEWEST, HB_MIN_OLD)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARGOCD_VERSIONS="${ARGOCD_VERSIONS:-v3.5.6 v3.3.18}"
KEEP="${KEEP:-0}"
HB_MIN_NEWEST="${HB_MIN_NEWEST:-15}" # minutes of per-minute heartbeat, first version
HB_MIN_OLD="${HB_MIN_OLD:-6}"        # same, other versions
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

assert() { # DESC CMD...
  local desc=$1; shift
  if "$@"; then ev "ASSERT ok: $desc"; else fail "assert: $desc"; fi
}

# Save the logger output of every logger Pod that exists now (old Pods keep the
# file from the last grab).
grab() {
  local p
  for p in $(k -n babis-spike get pods -l app=hub-logger -o name 2>/dev/null); do
    k -n babis-spike logs "$p" >"$VD/parts/${p#pod/}.jsonl" 2>/dev/null || true
  done
}

# Same redis workaround as test/e2e/run.sh.
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

# The spike template body. Same as the 0002 draft, plus reconciledAt for
# diagnosis. Quoted so that the shell keeps the template text as it is.
BODY='{"edge": "spike-edge", "event": "{{if .app.metadata.deletionTimestamp}}deleted{{else}}state{{end}}", "app": {"name": {{toJson .app.metadata.name}}, "sync": {{toJson .app.status.sync.status}}, "health": {{toJson .app.status.health.status}}, "revision": {{toJson .app.status.sync.revision}}, "reconciledAt": {{toJson .app.status.reconciledAt}}}}'

# apply_config MODE SLICE_END. MODE draft = the 0002 spec expressions as they
# are; MODE fixed = nil-safe expressions. SLICE_END is 15 (10 min bucket) or 16
# (1 min bucket) of the reconciledAt string. One template per trigger only so that the
# logger can tell the triggers apart (query string "?trigger="). The bodies are
# identical.
apply_config() {
  local mode=$1 end=$2 t once_change once_hb
  if [ "$mode" = draft ]; then
    once_change='app.status.sync.status + app.status.health.status + app.status.sync.revision'
    once_hb="app.status.reconciledAt[0:$end]"
  else
    once_change='(app.status.sync?.status ?? "") + (app.status.health?.status ?? "") + (app.status.sync?.revision ?? "")'
    once_hb="(app.status.reconciledAt ?? \"\")[0:$end]"
  fi
  {
    echo "data:"
    echo "  service.webhook.babis: |"
    echo "    url: http://hub-logger.babis-spike.svc:8080"
    echo "    headers:"
    echo "    - name: Authorization"
    echo "      value: Bearer \$babis-token"
    echo "    - name: Content-Type"
    echo "      value: application/json"
    for t in change heartbeat deleted; do
      echo "  template.babis-$t: |"
      echo "    webhook:"
      echo "      babis:"
      echo "        method: POST"
      echo "        path: /api/v1alpha1/app-events?trigger=$t"
      echo "        body: |"
      echo "          $BODY"
    done
    echo "  trigger.babis-change: |"
    echo "    - when: \"true\""
    echo "      oncePer: $once_change"
    echo "      send: [babis-change]"
    echo "  trigger.babis-heartbeat: |"
    echo "    - when: \"true\""
    echo "      oncePer: $once_hb"
    echo "      send: [babis-heartbeat]"
    echo "  trigger.babis-deleted: |"
    echo "    - when: app.metadata.deletionTimestamp != nil"
    echo "      oncePer: app.metadata.deletionTimestamp"
    echo "      send: [babis-deleted]"
    echo "  subscriptions: |"
    echo "    - recipients: [babis]"
    echo "      triggers: [babis-change, babis-heartbeat, babis-deleted]"
  } >"$VD/notifications-cm.yaml"
  k -n argocd patch configmap argocd-notifications-cm --type=merge --patch-file "$VD/notifications-cm.yaml" >/dev/null
  k -n argocd rollout restart deploy/argocd-notifications-controller >/dev/null
  k -n argocd rollout status deploy/argocd-notifications-controller --timeout=180s >/dev/null
}

save_notif_log() { k -n argocd logs deploy/argocd-notifications-controller --tail=-1 >"$VD/notif-$1.log" 2>&1 || true; }

app_manifest() { # NAME PATH [finalizer]
  local fin=""
  [ -z "${3:-}" ] || fin="  finalizers: [resources-finalizer.argocd.argoproj.io]"
  cat <<APP
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: $1
  namespace: argocd
$fin
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
apps_ok() { app_ok guestbook-1 && app_ok guestbook-2 && app_ok guestbook-3 && app_ok fin-app; }
app_gone() { ! k -n argocd get app "$1" >/dev/null 2>&1; }
all_have_events() { grab; local a; for a in guestbook-1 guestbook-2 guestbook-3 fin-app; do [ "$(an count "$a" change)" -ge 1 ] || return 1; done; }
late_event() { grab; [ "$(an count late-app change)" -ge 1 ]; }
hb_twice() { grab; [ "$(an hbbuckets guestbook-1 "$T_FIXED")" -ge 2 ]; }
logger_gone() { [ -z "$(k -n babis-spike get pods -l app=hub-logger -o name)" ]; }
got_late_outofsync() { grab; [ "$(an after late-app OutOfSync "$T_DOWN")" -ge 1 ]; }
got_outofsync() { grab; [ "$(an after guestbook-1 OutOfSync "$T_DOWN")" -ge 1 ]; }

run_version() {
  local VER=$1 HBMIN=$2 start=$SECONDS
  local CLUSTER="babis-spike-${VER#v}"
  CLUSTER="${CLUSTER//./-}"
  local VD="$OUT/$VER" T_DOWN="" T_UP="" T_FIXED=""
  mkdir -p "$VD/parts"
  : >"$VD/evidence.txt"
  export KUBECONFIG="$VD/kubeconfig"
  log "=== ArgoCD $VER (cluster $CLUSTER, heartbeat window ${HBMIN} min) ==="

  section "8. Notifications controller in core-install.yaml ($VER)"
  curl -fsSL --retry 3 -o "$VD/core-install.yaml" \
    "https://raw.githubusercontent.com/argoproj/argo-cd/${VER}/manifests/core-install.yaml"
  ev "grep -n 'argocd-notifications-controller' core-install.yaml (first 4 lines):"
  grep -n 'argocd-notifications-controller' "$VD/core-install.yaml" | sed -n '1,4p' | tee -a "$VD/evidence.txt"
  assert "core-install.yaml has the notifications controller" grep -q 'name: argocd-notifications-controller' "$VD/core-install.yaml"

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

  # Request logger.
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

  # Notifications config, before the Applications exist (we want first events).
  k -n argocd patch secret argocd-notifications-secret --type=merge \
    -p "{\"stringData\":{\"babis-token\":\"$TOKEN\"}}" >/dev/null
  apply_config draft 15
  log "notifications configured (draft expressions, heartbeat [0:15])"

  # Applications.
  local a
  for a in guestbook-1 guestbook-2 guestbook-3; do app_manifest "$a" guestbook | k apply -f - >/dev/null; done
  app_manifest bad-path does-not-exist | k apply -f - >/dev/null
  app_manifest fin-app guestbook finalizer | k apply -f - >/dev/null
  local t_apps
  t_apps="$(now)"
  wait_for "4 good apps Synced/Healthy" 420 apps_ok
  wait_for "first change event for the 4 good apps" 300 all_have_events
  sleep 45 # time for the bad-path app and for a second reconcile
  grab

  section "1. \$babis-token inside a header value"
  ev "spike token: $TOKEN"
  an auth "$TOKEN" | tee -a "$VD/evidence.txt" || ev "RESULT: Authorization header does NOT carry the secret"
  assert "every request has Authorization: Bearer <secret value>" an auth "$TOKEN"

  section "3. Global subscriptions (no annotation on the Applications)"
  ev "subscribe annotations on the Applications: $(k -n argocd get app -o json | jq -c '[.items[] | .metadata.annotations // {} | keys[] | select(startswith("notifications.argoproj.io/subscribe"))]')"
  ev "requests per app and trigger so far:"
  an summary | tee -a "$VD/evidence.txt"
  for a in guestbook-1 guestbook-2 guestbook-3 fin-app; do
    assert "events arrive for $a" test "$(an count "$a" change)" -ge 1
  done
  ev "bad-path app events: change=$(an count bad-path change) heartbeat=$(an count bad-path heartbeat)"

  section "4. toJson on missing status fields"
  an bodies | tee -a "$VD/evidence.txt"
  ev "first events of guestbook-1 (just-created app):"
  an lines guestbook-1 "" 4 | tee -a "$VD/evidence.txt"
  ev "events of bad-path:"
  an lines bad-path "" 4 | tee -a "$VD/evidence.txt"
  ev "Application status of bad-path:"
  k -n argocd get app bad-path -o json | jq -c '{sync: .status.sync, health: .status.health, reconciledAt: .status.reconciledAt, conditions: [.status.conditions[]? | {type, message: .message[0:90]}]}' | tee -a "$VD/evidence.txt"
  save_notif_log early
  ev "notifications controller log lines about errors (first 6):"
  { grep -iE 'error|fail|warn' "$VD/notif-early.log" || true; } | cut -c1-300 | sed -n '1,6p' | tee -a "$VD/evidence.txt"

  section "2a. oncePer with app.status.reconciledAt[0:15] (draft expression)"
  ev "heartbeat trigger TRIGGERED log lines: $(grep -c "Trigger 'babis-heartbeat' TRIGGERED" "$VD/notif-early.log")"
  ev "oncePer errors in the controller log (draft expressions): $(grep -c 'failed to execute oncePer' "$VD/notif-early.log")"
  ev "distinct oncePer error messages:"
  { grep 'failed to execute oncePer' "$VD/notif-early.log" || true; } | jq -r '.msg' | cut -c1-120 | sort | uniq -c | tee -a "$VD/evidence.txt"
  assert "draft [0:15] heartbeat trigger fires" test "$(grep -c "Trigger 'babis-heartbeat' TRIGGERED" "$VD/notif-early.log")" -ge 1

  section "4b. First events of a just-created app (nil-safe config)"
  apply_config fixed 15
  local t_late
  t_late="$(now)"
  T_FIXED="$t_late"
  app_manifest late-app guestbook | k apply -f - >/dev/null
  wait_for "late-app first event" 180 late_event
  sleep 20; grab
  ev "events of late-app since creation ($t_late):"
  an lines late-app "$t_late" 8 | tee -a "$VD/evidence.txt"
  an bodies | tee -a "$VD/evidence.txt"
  assert "all bodies are valid JSON" test "$(an bodies | sed -n '1,1p' | grep -c ' 0 invalid')" -eq 1

  section "5. Deletes"
  local t_del
  t_del="$(now)"
  k -n argocd delete app fin-app --wait=false >/dev/null
  wait_for "fin-app gone" 180 app_gone fin-app
  sleep 30; grab
  ev "events for fin-app (with finalizer) since the delete command at $t_del:"
  an lines fin-app "$t_del" 6 | tee -a "$VD/evidence.txt"
  ev "events with event=deleted:"
  an deleted | tee -a "$VD/evidence.txt"
  t_del="$(now)"
  k -n argocd delete app guestbook-3 --wait=false >/dev/null
  wait_for "guestbook-3 gone" 180 app_gone guestbook-3
  sleep 60; grab
  ev "events for guestbook-3 (no finalizer) since the delete command at $t_del:"
  an lines guestbook-3 "$t_del" 6 | tee -a "$VD/evidence.txt"
  ev "(empty above = nothing arrived)"

  section "2a (cont). [0:15]: a second heartbeat after the 10 min bucket changes (since the nil-safe config at $T_FIXED)"
  wait_for "heartbeat events for guestbook-1 with 2 different [0:15] buckets" 1200 hb_twice
  an hb guestbook-1 "$T_FIXED" | tee -a "$VD/evidence.txt"
  assert "[0:15] fires again when the bucket changes" test "$(an hbbuckets guestbook-1 "$T_FIXED")" -ge 2

  section "2b. Heartbeat rate with [0:16] (per minute), window ${HBMIN} min"
  apply_config fixed 16
  sleep 15 # the config switch itself sends one burst of events
  local t_win
  t_win="$(now)"
  k -n argocd get app guestbook-1 -w -o json --show-managed-fields >"$VD/watch.json" 2>/dev/null &
  local wpid=$!
  sleep $((HBMIN * 60))
  kill "$wpid" 2>/dev/null || true
  grab
  ev "window: $t_win to $(now) UTC; reconcile settings: $(k -n argocd get cm argocd-cm -o jsonpath='{.data.timeout\.reconciliation}') (empty = default 120s)"
  ev "heartbeat intervals per app in the window:"
  an intervals heartbeat "$t_win" | tee -a "$VD/evidence.txt"
  ev "change-trigger intervals (should be none unless state changed):"
  an intervals change "$t_win" | tee -a "$VD/evidence.txt"
  ev "heartbeat events of guestbook-1 in the window:"
  an lines guestbook-1 "$t_win" 60 | { grep ' heartbeat ' || true; } | tee -a "$VD/evidence.txt"
  for a in guestbook-1 guestbook-2 bad-path; do
    ev "events in window: $a heartbeat=$(an count "$a" heartbeat "$t_win") change=$(an count "$a" change "$t_win")"
  done

  section "6. Edge cost: annotation and resourceVersion changes (guestbook-1, same window)"
  ev "annotations on guestbook-1 now:"
  k -n argocd get app guestbook-1 -o json | jq -c '.metadata.annotations' | cut -c1-600 | tee -a "$VD/evidence.txt"
  an rv "$VD/watch.json" | tee -a "$VD/evidence.txt"
  ev "Application guestbook-1 now: $(k -n argocd get app guestbook-1 -o json | an annot)"
  ev "heartbeat events sent for guestbook-1 in the window: $(an count guestbook-1 heartbeat "$t_win")"

  section "Requests per change (guestbook-2 scaled to 2 replicas)"
  local t_chg
  t_chg="$(now)"
  k -n ns-guestbook-2 scale deploy/guestbook-ui --replicas=2 >/dev/null
  sleep 100; grab
  ev "all requests for guestbook-2 since $t_chg:"
  an lines guestbook-2 "$t_chg" 30 | tee -a "$VD/evidence.txt"

  section "7a. Flap: guestbook-2 goes back to a state it had before (scale 2 -> 1)"
  local t_flap
  t_flap="$(now)"
  k -n ns-guestbook-2 scale deploy/guestbook-ui --replicas=1 >/dev/null
  sleep 150; grab
  ev "app state now: $(app_state guestbook-2)"
  ev "requests for guestbook-2 since $t_flap (none for 'Synced/Healthy' = repeat state is deduplicated):"
  an lines guestbook-2 "$t_flap" 30 | tee -a "$VD/evidence.txt"
  ev "Synced events for guestbook-2 since $t_flap: $(an after guestbook-2 Synced "$t_flap")"

  section "7. Hub down for 2 minutes with a change"
  grab
  k -n babis-spike scale deploy/hub-logger --replicas=0 >/dev/null
  wait_for "logger Pod gone" 120 logger_gone
  T_DOWN="$(now)"
  k -n ns-guestbook-1 scale deploy/guestbook-ui --replicas=3 >/dev/null
  ev "logger down at $T_DOWN; guestbook-1 Deployment scaled to 3 (app becomes OutOfSync)"
  sleep 120
  save_notif_log hubdown
  k -n babis-spike scale deploy/hub-logger --replicas=1 >/dev/null
  k -n babis-spike rollout status deploy/hub-logger --timeout=180s >/dev/null
  T_UP="$(now)"
  ev "logger back at $T_UP"
  wait_for "OutOfSync event for guestbook-1 arrives after the outage" 600 got_outofsync
  ev "events for guestbook-1 since logger came back ($T_UP):"
  an lines guestbook-1 "$T_UP" 8 | tee -a "$VD/evidence.txt"
  ev "events for guestbook-1 during the outage ($T_DOWN to $T_UP, from the old Pod's saved log):"
  an lines guestbook-1 "$T_DOWN" 100 | awk -v up="$T_UP" '$1 < up' | tee -a "$VD/evidence.txt"
  ev "notifications controller log lines about failed sends during the outage (first 6):"
  save_notif_log hubup
  ev "controller log lines between $T_DOWN and the next processing after recovery (full log of the last minutes):"
  { grep -E 'ERR|retry|Failed|timed out|Sending notification' "$VD/notif-hubup.log" || true; } | tail -n 10 | cut -c1-420 | tee -a "$VD/evidence.txt"
  section "7b. Hub unreachable fast (DNS failure) for 90 s, with a change (late-app scaled to 2)"
  k -n argocd patch cm argocd-notifications-cm --type=merge \
    -p '{"data":{"service.webhook.babis":"url: http://nonexistent-hub.babis-spike.svc:8080\nheaders:\n- name: Authorization\n  value: Bearer $babis-token\n- name: Content-Type\n  value: application/json\n"}}' >/dev/null
  sleep 5
  T_DOWN="$(now)"
  k -n ns-late-app scale deploy/guestbook-ui --replicas=2 >/dev/null
  sleep 90
  save_notif_log dns
  ev "notifications controller log lines for late-app during the DNS outage (errors, then retry):"
  { grep -E 'nonexistent-hub|Failed to notify' "$VD/notif-dns.log" || true; } | sed -n '1,8p' | cut -c1-420 | tee -a "$VD/evidence.txt"
  ev "late-app annotation keys before restore: $(k -n argocd get app late-app -o json | an annot)"
  k -n argocd patch cm argocd-notifications-cm --type=merge \
    -p '{"data":{"service.webhook.babis":"url: http://hub-logger.babis-spike.svc:8080\nheaders:\n- name: Authorization\n  value: Bearer $babis-token\n- name: Content-Type\n  value: application/json\n"}}' >/dev/null
  T_UP="$(now)"
  ev "hub URL restored at $T_UP"
  wait_for "late-app OutOfSync event after the DNS outage" 400 got_late_outofsync
  ev "events for late-app since the outage began ($T_DOWN):"
  an lines late-app "$T_DOWN" 8 | tee -a "$VD/evidence.txt"
  grab
  save_notif_log final
  ev "final request summary:"
  an summary | tee -a "$VD/evidence.txt"
  section "Done ArgoCD $VER in $((SECONDS - start))s"
  echo "$((SECONDS - start))" >"$VD/seconds"

  if [ "$KEEP" != "1" ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
}

first=1
for v in $ARGOCD_VERSIONS; do
  c="babis-spike-${v#v}"; CLUSTERS+=("${c//./-}")
  hb=$HB_MIN_OLD; [ "$first" = 1 ] && hb=$HB_MIN_NEWEST; first=0
  mkdir -p "$OUT/$v"
  ( run_version "$v" "$hb" ) >"$OUT/$v.log" 2>&1 &
  PIDS+=($!)
done

rc=0
i=0
for v in $ARGOCD_VERSIONS; do
  if wait "${PIDS[$i]}"; then
    echo "##### $v: PASS in $(cat "$OUT/$v/seconds")s"
    cat "$OUT/$v/evidence.txt"
  else
    echo "##### $v: FAIL (last 40 log lines of $OUT/$v.log)"
    tail -40 "$OUT/$v.log"
    rc=1
  fi
  i=$((i + 1))
done
PIDS=()
[ $rc -eq 0 ] && echo "PASS: all versions ($ARGOCD_VERSIONS); evidence in $OUT"
exit $rc
