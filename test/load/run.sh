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
HUB=http://127.0.0.1:8090
OUT=test/load/last-run.json
WORK="$(mktemp -d)"
export KUBECONFIG="$WORK/kubeconfig"
export BABIS_INGEST_TOKEN BABIS_TOKEN
BABIS_INGEST_TOKEN="$(openssl rand -hex 16)"
BABIS_TOKEN="$BABIS_INGEST_TOKEN"

OP_PID=""
CREATED=0
log() { echo "[$(date +%H:%M:%S)] $*"; }

cleanup() {
  local rc=$?
  [ -z "$OP_PID" ] || kill "$OP_PID" 2>/dev/null || true
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

case " $SCENARIOS " in *" A "*) scenario A 0;; esac
case " $SCENARIOS " in *" B "*) scenario B "$SPREAD_B";; esac

jq --arg d "$(date -u +%FT%TZ)" --arg c "$(git rev-parse --short HEAD)" \
   '{date:$d, commit:$c, scenarios:.}' "$RESULTS" >"$OUT"

echo
printf '%-4s %-5s %7s %6s %-22s %7s %7s %7s %7s %6s %9s\n' scen round reqs ok errors p50ms p99ms maxms wall_s edges VmHWM_MB
jq -r '.scenarios[] | . as $s | .rounds[] |
  [$s.scenario, .round, .requests, .ok, ((.errors|to_entries|map("\(.key)=\(.value)")|join(","))|if .=="" then "-" else . end),
   (.p50_ms|floor), (.p99_ms|floor), (.max_ms|floor), (.wall_s*10|floor/10), $s.edges_counted, ($s.operator_vmhwm_kb/1024|floor)] | @tsv' "$OUT" |
  while IFS=$'\t' read -r a b c d e f g h i j k; do
    printf '%-4s %-5s %7s %6s %-22s %7s %7s %7s %7s %6s %9s\n' "$a" "$b" "$c" "$d" "$e" "$f" "$g" "$h" "$i" "$j" "$k"
  done
echo "operator log errors per scenario: $(jq -c '[.scenarios[] | {(.scenario): .operator_log_errors}] | add' "$OUT")"
for f in test/load/last-operator-*.log; do
  echo "error kinds in $f:"
  grep '"level":"error"' "$f" | jq -r '.msg + " | " + ((.error // "")[0:140])' 2>/dev/null \
    | sed -E 's/"[a-z0-9.-]+-[0-9]{5}"/"EDGE"/g' | sort | uniq -c | sort -rn | head -5 || true
done
exit $FAILED
