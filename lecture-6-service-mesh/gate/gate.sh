#!/usr/bin/env bash
set -euo pipefail

# All thresholds and deployment settings can be overridden through environment.
PROMETHEUS_URL=${PROMETHEUS_URL:-http://127.0.0.1:8081}
# Optional ingress Host header, e.g. prometheus.local with URL http://127.0.0.1:8081.
PROMETHEUS_HOST=${PROMETHEUS_HOST:-prometheus.local}
NAMESPACE=${NAMESPACE:-api}
VS_NAME=${VS_NAME:-api-route}
API_HOST=${API_HOST:-api-lab3.api.svc.cluster.local}
V2_DEPLOYMENT=${V2_DEPLOYMENT:-api-v2}
API_METRICS_JOB=${API_METRICS_JOB:-api-lab3}
INTERVAL_SECONDS=${INTERVAL_SECONDS:-15}
WINDOW=${WINDOW:-1m}
MIN_REQUESTS=${MIN_REQUESTS:-20}
MAX_ERROR_RATE=${MAX_ERROR_RATE:-0.05} # Fraction: 0.05 = 5%.
HEALTHY_CHECKS=${HEALTHY_CHECKS:-4}
PROMOTION_STEP=${PROMOTION_STEP:-15}
SETTLE_SECONDS=${SETTLE_SECONDS:-60} # Let the metric window reflect the new weight.
CURL_TIMEOUT_SECONDS=${CURL_TIMEOUT_SECONDS:-10}
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
TEMPLATE=${TEMPLATE:-$SCRIPT_DIR/virtualservice.yaml.template}

log() { printf '%s %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"; }
for dependency in curl jq kubectl envsubst; do
  command -v "$dependency" >/dev/null || { log "Missing dependency: $dependency"; exit 1; }
done
# Validate configurable numbers and PromQL inputs before any mutation.
for number in "$INTERVAL_SECONDS" "$MIN_REQUESTS" "$HEALTHY_CHECKS" "$PROMOTION_STEP" "$SETTLE_SECONDS" "$CURL_TIMEOUT_SECONDS"; do
  [[ $number =~ ^[1-9][0-9]*$ ]] || { log "Expected positive integer, got: $number"; exit 1; }
done
jq -en --arg e "$MAX_ERROR_RATE" \
  '($e|tonumber) >= 0 and ($e|tonumber) <= 1' >/dev/null
[[ $WINDOW =~ ^[1-9][0-9]*[smh]$ && $V2_DEPLOYMENT =~ ^[a-z0-9-]+$ && $API_METRICS_JOB =~ ^[a-zA-Z0-9_./-]+$ && $NAMESPACE =~ ^[a-z0-9-]+$ ]] || exit 1
[[ -f $TEMPLATE ]] || { log "Template not found: $TEMPLATE"; exit 1; }

query() {
  local headers=()
  if [[ -n $PROMETHEUS_HOST ]]; then
    headers=(-H "Host: $PROMETHEUS_HOST")
  fi
  curl --fail --silent --show-error --max-time "$CURL_TIMEOUT_SECONDS" \
    "${headers[@]}" \
    --get "${PROMETHEUS_URL%/}/api/v1/query" --data-urlencode "query=$1" |
    jq -er 'select(.status == "success") | .data.result |
      select(length == 1) | .[0].value[1] | tonumber |
      select(isfinite and . >= 0)'
}

# jq handles fractional values that Bash arithmetic cannot compare.
at_least() {
  jq -en --argjson value "$1" --argjson limit "$2" '$value >= $limit' >/dev/null
}

above() {
  jq -en --argjson value "$1" --argjson limit "$2" '$value > $limit' >/dev/null
}

read_metrics() {
  count=unavailable
  errors=unavailable
  local value
  if ! value=$(query "$count_query"); then
    log "METRICS ERROR: request count unavailable"
    return 1
  fi
  count=$value
  if ! value=$(query "$error_query"); then
    log "METRICS ERROR: error rate unavailable; requests=$count"
    return 1
  fi
  errors=$value
}

read_current_weight() {
  kubectl get virtualservice "$VS_NAME" -n "$NAMESPACE" -o json |
    jq -er --arg host "$API_HOST" '
      select(.spec.hosts == [$host]) |
      .spec.http | select(length == 1) | .[0].route |
      select(length == 2 and all(.[]; .destination.host == $host)) |
      map({key: .destination.subset, value: .weight}) | from_entries |
      select(.v1 >= 0 and .v2 >= 0 and (.v1 + .v2) == 100) | .v2
    '
}

apply_weights() {
  V2_WEIGHT=$1
  V1_WEIGHT=$((100 - V2_WEIGHT))
  export VS_NAME NAMESPACE API_HOST V1_WEIGHT V2_WEIGHT
  log "APPLY: $NAMESPACE/$VS_NAME requested v1=$V1_WEIGHT v2=$V2_WEIGHT"
  # Substitute only our variables; never evaluate shell text from the template.
  # shellcheck disable=SC2016
  envsubst '${VS_NAME} ${NAMESPACE} ${API_HOST} ${V1_WEIGHT} ${V2_WEIGHT}' \
    < "$TEMPLATE" > "$manifest"
  if ! kubectl apply -f "$manifest"; then
    log "APPLY ERROR: failed to update VirtualService; stopping"
    exit 1
  fi
  local actual
  if ! actual=$(read_current_weight); then
    log "VERIFY ERROR: cannot read applied weights; stopping"
    exit 1
  fi
  log "VERIFY: VirtualService v1=$((100 - actual)) v2=$actual"
  if (( actual != V2_WEIGHT )); then
    log "VERIFY ERROR: requested v2=$V2_WEIGHT but found v2=$actual; stopping"
    exit 1
  fi
}

manifest=$(mktemp)
trap 'rm -f "$manifest"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Resume the existing split; do not restart a finished/rolled-back release.
weight=$(read_current_weight)
[[ $weight =~ ^[0-9]+$ ]] || exit 1
if (( weight == 0 || weight == 100 )); then
  log "Terminal split v2=$weight; set a starting split (e.g. 90/10) before a new run"
  exit 0
fi

# These are application counters scraped directly from API pods, not Envoy.
# The shared Service scrapes v1 and v2 under one job; identify v2 by Deployment.
# Restrict to orders so successful health probes cannot dilute the error rate.
labels="namespace=\"$NAMESPACE\",job=\"$API_METRICS_JOB\",pod=~\"$V2_DEPLOYMENT-.*\",method=\"POST\",path=\"/order\""
count_query="sum(increase(http_requests_total{$labels}[$WINDOW]))"
error_query="(sum(rate(http_requests_total{$labels,code=~\"5..\"}[$WINDOW])) or vector(0)) / sum(rate(http_requests_total{$labels}[$WINDOW]))"
healthy=0
log "START: $NAMESPACE/$VS_NAME v1=$((100 - weight)) v2=$weight"
log "CONFIG: window=$WINDOW interval=${INTERVAL_SECONDS}s min_requests=$MIN_REQUESTS error_limit=$MAX_ERROR_RATE healthy_checks=$HEALTHY_CHECKS step=$PROMOTION_STEP"
log "METRICS: http_requests_total{$labels}"
log "WAIT: settling for ${SETTLE_SECONDS}s"
sleep "$SETTLE_SECONDS"
while true; do
  sleep "$INTERVAL_SECONDS"
  log "CHECK: reading weights and backend metrics"
  actual=$(read_current_weight)
  if (( actual != weight )); then
    log "STOP: weights changed externally; expected v2=$weight actual v2=$actual"
    exit 1
  fi

  if ! read_metrics; then
    healthy=0
    log "HOLD: missing/invalid metrics; v1=$((100 - weight)) v2=$weight healthy=0/$HEALTHY_CHECKS"
    continue
  fi

  if ! at_least "$count" "$MIN_REQUESTS"; then
    healthy=0
    log "HOLD: requests=$count minimum=$MIN_REQUESTS error_rate=$errors; v1=$((100 - weight)) v2=$weight healthy=0/$HEALTHY_CHECKS"
    continue
  fi

  error_percent=$(jq -n --argjson rate "$errors" '$rate * 100')
  log "METRICS: requests=$count error_rate=$errors (${error_percent}%) limit=$MAX_ERROR_RATE v1=$((100 - weight)) v2=$weight"

  if above "$errors" "$MAX_ERROR_RATE"; then
    log "DECISION: rollback; error_rate=$errors exceeds limit=$MAX_ERROR_RATE"
    apply_weights 0
    log "ROLLBACK: unhealthy v2; stopping"
    exit 0
  fi

  healthy=$((healthy + 1))
  log "HEALTHY: checks=$healthy/$HEALTHY_CHECKS"
  if (( healthy < HEALTHY_CHECKS )); then
    log "HOLD: waiting for more healthy checks; weights unchanged"
    continue
  fi

  previous_weight=$weight
  weight=$((weight + PROMOTION_STEP))
  if (( weight > 100 )); then
    weight=100
  fi
  log "DECISION: promote v2=$previous_weight -> $weight"
  apply_weights "$weight"

  if (( weight == 100 )); then
    log "PROMOTED: v2=100%; stopping"
    exit 0
  fi

  healthy=0
  log "WAIT: settling for ${SETTLE_SECONDS}s"
  sleep "$SETTLE_SECONDS"
done
