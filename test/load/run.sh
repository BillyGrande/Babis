#!/usr/bin/env bash
# Load test: simulated edges against the operator running on the host,
# with a kind cluster as the API server.
# Usage: make load   (or: EDGES=500 KEEP=1 bash test/load/run.sh)
# OPERATOR_FLAGS="--kube-api-qps 100 --kube-api-burst 200" passes extra flags to the operator.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

KEEP="${KEEP:-0}"
CLUSTER="${CLUSTER:-babis-load}"
NS=babis-fleet
EDGES="${EDGES:-1000}"
APPS="${APPS:-300}"
ROUNDS="${ROUNDS:-2}"
SPREAD_B="${SPREAD_B:-60s}"
SCENARIOS="${SCENARIOS:-A B}"
RATE="${RATE:-500}"   # scenario C: app events per second
HUB_B=http://127.0.0.1:8091
FLUSH_WAIT="${FLUSH_WAIT:-90}"   # scenario C: seconds to wait for the last flush (2 x 30s + margin)
HUB=http://127.0.0.1:8090
OUT=test/load/last-run.json
WORK="$(mktemp -d)"
export KUBECONFIG="$WORK/kubeconfig"
export BABIS_INGEST_TOKEN BABIS_TOKEN
BABIS_INGEST_TOKEN="$(openssl rand -hex 16)"
BABIS_TOKEN="$BABIS_INGEST_TOKEN"

OP_PID=""
OP2_PID=""
CREATED=0
log() { echo "[$(date +%H:%M:%S)] $*"; }

cleanup() {
  local rc=$?
  [ -z "$OP_PID" ] || kill "$OP_PID" 2>/dev/null || true
  [ -z "$OP2_PID" ] || kill "$OP2_PID" 2>/dev/null || true
  if [ "$CREATED" = 1 ] && [ "$KEEP" != "1" ]; then
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  elif [ "$CREATED" = 1 ]; then
    kind get kubeconfig --name "$CLUSTER" >"$ROOT/test/load/kubeconfig" 2>/dev/null || true
    echo "KEEP=1: cluster $CLUSTER kept (context kind-$CLUSTER)"
  fi
  rm -rf "$WORK"
  exit $rc
}
trap cleanup EXIT

if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  if [ "$KEEP" = "1" ]; then log "reusing cluster $CLUSTER"; else
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1
    log "creating cluster $CLUSTER"; kind create cluster --name "$CLUSTER" --wait 120s >/dev/null 2>&1
  fi
else
  log "creating cluster $CLUSTER"; kind create cluster --name "$CLUSTER" --wait 120s >/dev/null 2>&1
fi
CREATED=1
kind get kubeconfig --name "$CLUSTER" >"$KUBECONFIG"

kubectl apply -f central/config/crd/fleet.babis.dev_edges.yaml >/dev/null
kubectl get ns "$NS" >/dev/null 2>&1 || kubectl create ns "$NS" >/dev/null
kubectl delete edges --all -n "$NS" --wait=true >/dev/null

# Operator and load generator only: the edge binary is not needed here.
log "building"
mkdir -p bin
(cd central && CGO_ENABLED=0 go build -trimpath -o ../bin/babis-operator ./cmd/babis-operator \
  && CGO_ENABLED=0 go build -trimpath -o ../bin/babis-loadgen ./cmd/babis-loadgen)

OPLOG=""
# start_operator N: fresh operator per scenario, so VmHWM and the log are per scenario.
start_operator() {
  OPLOG="$WORK/operator-$1.log"
  bin/babis-operator --leader-elect=false --fleet-namespace="$NS" \
    --metrics-addr=:18080 --health-addr=:18081 ${OPERATOR_FLAGS:-} >"$OPLOG" 2>&1 &
  OP_PID=$!
  for _ in $(seq 60); do
    curl -fsS "$HUB/healthz" >/dev/null 2>&1 && break
    kill -0 "$OP_PID" 2>/dev/null || { cat "$OPLOG" >&2; echo "operator exited" >&2; exit 1; }
    sleep 1
  done
  curl -fsS "$HUB/healthz" >/dev/null || { echo "operator not healthy" >&2; exit 1; }
  log "operator up (pid $OP_PID)"
}

RESULTS="$WORK/results.json"
echo '[]' >"$RESULTS"
FAILED=0

scenario() {
  local name=$1 spread=$2
  log "scenario $name: edges=$EDGES apps=$APPS rounds=$ROUNDS spread=$spread"
  local rounds_json="$WORK/$name.json"
  start_operator "$name"
  bin/babis-loadgen --hub "$HUB" --edges "$EDGES" --apps "$APPS" --rounds "$ROUNDS" \
    --spread "$spread" --json >"$rounds_json"
  sleep 3
  local count rss errs
  count="$(kubectl get edges -n "$NS" --no-headers 2>/dev/null | wc -l)"
  rss="$(awk '/VmHWM/ {print $2}' "/proc/$OP_PID/status")"
  errs="$(grep -c '"level":"error"' "$OPLOG" || true)"
  [ "$count" = "$EDGES" ] || { log "FAIL: $count Edges, want $EDGES"; FAILED=1; }
  jq --arg n "$name" --arg sp "$spread" --argjson c "$count" --argjson rss "$rss" \
     --argjson e "$errs" --slurpfile r "$rounds_json" \
     '. + [{scenario:$n, spread:$sp, edges_counted:$c, operator_vmhwm_kb:$rss, operator_log_errors:$e, rounds:$r[0]}]' \
     "$RESULTS" >"$RESULTS.new" && mv "$RESULTS.new" "$RESULTS"
  kill "$OP_PID" 2>/dev/null || true; wait "$OP_PID" 2>/dev/null || true; OP_PID=""
  cp "$OPLOG" "test/load/last-operator-$name.log"
  kubectl delete edges --all -n "$NS" --wait=true >/dev/null
}

# patch_stats PORT: "<PATCH requests> <PATCH 409s>" from an operator's metrics endpoint.
patch_stats() {
  curl -fsS "http://127.0.0.1:$1/metrics" | awk '
    /^rest_client_requests_total\{/ && /method="PATCH"/ { n += $NF; if ($0 ~ /code="409"/) c += $NF }
    END { printf "%d %d\n", n, c }'
}

# scenario_c: two replicas, leader election on, app events at RATE per second.
scenario_c() {
  local name=C
  log "scenario $name: edges=$EDGES apps=$APPS rate=$RATE/s, two operators"
  local log1="$WORK/operator-C1.log" log2="$WORK/operator-C2.log"
  bin/babis-operator --leader-elect=true --fleet-namespace="$NS" --ingest-addr=:8090 \
    --metrics-addr=:18080 --health-addr=:18081 ${OPERATOR_FLAGS:-} >"$log1" 2>&1 &
  OP_PID=$!
  bin/babis-operator --leader-elect=true --fleet-namespace="$NS" --ingest-addr=:8091 \
    --metrics-addr=:18082 --health-addr=:18083 ${OPERATOR_FLAGS:-} >"$log2" 2>&1 &
  OP2_PID=$!
  for h in "$HUB" "$HUB_B"; do
    for _ in $(seq 60); do curl -fsS "$h/healthz" >/dev/null 2>&1 && break; sleep 1; done
    curl -fsS "$h/healthz" >/dev/null || { echo "operator at $h not healthy" >&2; exit 1; }
  done
  log "operators up (pids $OP_PID $OP2_PID)"
  local b1 b2 a1 a2
  b1="$(patch_stats 18080)"; b2="$(patch_stats 18082)"
  local run_json="$WORK/C.json" t0 t1
  t0=$(date +%s.%N)
  bin/babis-loadgen --mode events --hub "$HUB" --hub "$HUB_B" --edges "$EDGES" --apps "$APPS" \
    --rate "$RATE" --json >"$run_json"
  log "events sent; waiting up to ${FLUSH_WAIT}s for the flushes"
  local good=0 deadline=$((SECONDS + FLUSH_WAIT))
  while :; do
    good="$(kubectl get edges -n "$NS" --no-headers \
      -o custom-columns=T:.status.appsTotal,S:.status.telemetry.source 2>/dev/null \
      | awk -v a="$APPS" '$1 == a && $2 == "argocd-notifications" {n++} END {print n+0}')"
    [ "$good" = "$EDGES" ] && break
    [ "$SECONDS" -lt "$deadline" ] || break
    sleep 5
  done
  t1=$(date +%s.%N)
  a1="$(patch_stats 18080)"; a2="$(patch_stats 18082)"
  local count rss1 rss2
  count="$(kubectl get edges -n "$NS" --no-headers 2>/dev/null | wc -l)"
  rss1="$(awk '/VmHWM/ {print $2}' "/proc/$OP_PID/status")"
  rss2="$(awk '/VmHWM/ {print $2}' "/proc/$OP2_PID/status")"
  local nonok
  nonok="$(jq '.[0] | .requests - .ok' "$run_json")"
  [ "$nonok" = 0 ] || { log "FAIL: $nonok answers other than 202"; FAILED=1; }
  [ "$count" = "$EDGES" ] || { log "FAIL: $count Edges, want $EDGES"; FAILED=1; }
  [ "$good" = "$EDGES" ] || { log "FAIL: $good Edges with appsTotal=$APPS and source argocd-notifications, want $EDGES"; FAILED=1; }
  local p1 c1 p2 c2 wall
  read -r p1 c1 <<<"$(echo "$b1 $a1" | awk '{print $3-$1, $4-$2}')"
  read -r p2 c2 <<<"$(echo "$b2 $a2" | awk '{print $3-$1, $4-$2}')"
  wall="$(echo "$t1 $t0" | awk '{printf "%.1f", $1-$2}')"
  kill "$OP_PID" "$OP2_PID" 2>/dev/null || true; wait "$OP_PID" "$OP2_PID" 2>/dev/null || true
  OP_PID=""; OP2_PID=""
  local fl1 fl2 cf1 cf2
  fl1="$(grep -ci 'flush' <(grep '"level":"error"' "$log1") || true)"
  fl2="$(grep -ci 'flush' <(grep '"level":"error"' "$log2") || true)"
  cf1="$(grep -ci 'conflict\|the object has been modified' "$log1" || true)"
  cf2="$(grep -ci 'conflict\|the object has been modified' "$log2" || true)"
  jq --arg n "$name" --argjson c "$count" --argjson good "$good" --argjson r1 "$rss1" --argjson r2 "$rss2" \
     --argjson p1 "$p1" --argjson p2 "$p2" --argjson c1 "$c1" --argjson c2 "$c2" \
     --argjson fl1 "$fl1" --argjson fl2 "$fl2" --argjson cf1 "$cf1" --argjson cf2 "$cf2" \
     --argjson w "$wall" --argjson rate "$RATE" --slurpfile r "$run_json" \
     '. + [{scenario:$n, mode:"events", rate:$rate, edges_counted:$c, edges_complete:$good,
            operator_vmhwm_kb:[$r1,$r2], patch_requests:[$p1,$p2], patch_409:[$c1,$c2],
            patch_per_s:(($p1+$p2)/$w), wait_and_send_wall_s:$w,
            flush_error_lines:[$fl1,$fl2], conflict_log_lines:[$cf1,$cf2], rounds:$r[0]}]' \
     "$RESULTS" >"$RESULTS.new" && mv "$RESULTS.new" "$RESULTS"
  cp "$log1" test/load/last-operator-C1.log; cp "$log2" test/load/last-operator-C2.log
  log "C: PATCH $p1+$p2 (409: $c1+$c2), flush error lines $fl1+$fl2, conflict log lines $cf1+$cf2, VmHWM ${rss1}+${rss2} kB"
  kubectl delete edges --all -n "$NS" --wait=true >/dev/null
}

case " $SCENARIOS " in *" A "*) scenario A 0;; esac
case " $SCENARIOS " in *" B "*) scenario B "$SPREAD_B";; esac
case " $SCENARIOS " in *" C "*) scenario_c;; esac

jq --arg d "$(date -u +%FT%TZ)" --arg c "$(git rev-parse --short HEAD)" \
   '{date:$d, commit:$c, scenarios:.}' "$RESULTS" >"$OUT"

echo
printf '%-4s %-5s %7s %6s %-22s %7s %7s %7s %7s %6s %9s\n' scen round reqs ok errors p50ms p99ms maxms wall_s edges VmHWM_MB
jq -r '.scenarios[] | . as $s | .rounds[] |
  [$s.scenario, .round, .requests, .ok, ((.errors|to_entries|map("\(.key)=\(.value)")|join(","))|if .=="" then "-" else . end),
   (.p50_ms|floor), (.p99_ms|floor), (.max_ms|floor), (.wall_s*10|floor/10), $s.edges_counted, ([$s.operator_vmhwm_kb] | flatten | map(./1024|floor) | join("+"))] | @tsv' "$OUT" |
  while IFS=$'\t' read -r a b c d e f g h i j k; do
    printf '%-4s %-5s %7s %6s %-22s %7s %7s %7s %7s %6s %9s\n' "$a" "$b" "$c" "$d" "$e" "$f" "$g" "$h" "$i" "$j" "$k"
  done
echo "operator log errors per scenario: $(jq -c '[.scenarios[] | select(.operator_log_errors != null) | {(.scenario): .operator_log_errors}] | add // {}' "$OUT")"
for f in test/load/last-operator-*.log; do
  echo "error kinds in $f:"
  grep '"level":"error"' "$f" | jq -r '.msg + " | " + ((.error // "")[0:140])' 2>/dev/null \
    | sed -E 's/"[a-z0-9.-]+-[0-9]{5}"/"EDGE"/g' | sort | uniq -c | sort -rn | head -5 || true
done
exit $FAILED
