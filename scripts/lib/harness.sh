#!/bin/bash
# Shared smoke harness: an ephemeral demarkus server plus the adapter,
# on ports unique to the run. Source it, call harness_start, assert with
# check/refute, and finish with harness_report.
#
# harness_start <label> <api-key> [curl-max-time]
# Exports: SCRATCH, ADDR, CURL, API_KEY. Also sets SERVER_LOG, ADAPTER_LOG.
# Honors ADAPTER_NAV (auto|off|require) from the caller's environment.

SCRATCH=$(mktemp -d)
SERVER_PID=""
ADAPTER_PID=""
harness_cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  [ -n "$ADAPTER_PID" ] && kill "$ADAPTER_PID" 2>/dev/null || true
  wait 2>/dev/null || true
  rm -rf "$SCRATCH"
}
trap harness_cleanup EXIT

harness_start() {
  local label=$1 api_key=$2 max_time=${3:-30}
  local repo port http_port
  repo=$(cd "$(dirname "${BASH_SOURCE[1]}")/.." && pwd)

  port=$((20000 + RANDOM % 20000))
  http_port=$((20000 + RANDOM % 20000))
  if lsof -nP -iUDP:$port >/dev/null 2>&1 || lsof -nP -iTCP:$http_port >/dev/null 2>&1; then
    echo "port collision ($port/$http_port), rerun" >&2
    exit 1
  fi
  ADDR=127.0.0.1:$http_port
  API_KEY=$api_key
  SERVER_LOG="$SCRATCH/server.log"
  ADAPTER_LOG="$SCRATCH/adapter.log"
  CURL="curl -s --connect-timeout 5 --max-time $max_time"

  mkdir -p "$SCRATCH/root"
  local token
  token=$(~/.demarkus/bin/demarkus-token generate -label "$label" -ops read,publish -paths '/**' -tokens "$SCRATCH/tokens.toml" 2>/dev/null | tail -1)
  if [ -z "$token" ]; then
    echo "token generation failed" >&2
    exit 1
  fi

  ~/.demarkus/bin/demarkus-server -root "$SCRATCH/root" -port $port -tokens "$SCRATCH/tokens.toml" >"$SERVER_LOG" 2>&1 &
  SERVER_PID=$!

  # Built binary, not `go run`: killing go run's PID orphans the child.
  (cd "$repo" && go build -o "$SCRATCH/adapter" ./cmd/adapter)
  # ADAPTER_NAV passes through from the caller's environment.
  ADAPTER_ADDR=$ADDR DEMARKUS_HOST=localhost:$port DEMARKUS_INSECURE=1 DEMARKUS_TOKEN="$token" ADAPTER_API_KEY="$api_key" \
    "$SCRATCH/adapter" >"$ADAPTER_LOG" 2>&1 &
  ADAPTER_PID=$!

  local up=0
  attempts=20
  while [ "$attempts" -gt 0 ]; do
    $CURL -f "http://$ADDR/healthz" >/dev/null 2>&1 && up=1 && break
    attempts=$((attempts - 1))
    sleep 0.5
  done
  if [ "$up" != 1 ]; then
    echo "adapter never became healthy" >&2
    tail -5 "$ADAPTER_LOG" >&2
    return 1
  fi
}

# post <endpoint> <json>; appends HTTPSTATUS:<code> after the body.
post() {
  $CURL -w 'HTTPSTATUS:%{http_code}' -X POST "http://$ADDR/$1" \
    -H 'Content-Type: application/json' -H "X-Api-Key: $API_KEY" -d "$2"
}

pass=0
fail=0
# report <name> <ok?>
report() {
  if [ "$2" = 1 ]; then echo "PASS: $1"; pass=$((pass + 1)); else fail=$((fail + 1)); fi
}
check() { # name, expected-substring, actual
  if [[ "$3" == *"$2"* ]]; then report "$1" 1; else report "$1" 0; echo "FAIL: $1 -> $3"; fi
}
refute() { # name, forbidden-substring, actual
  if [[ "$3" != *"$2"* ]]; then report "$1" 1; else report "$1" 0; echo "FAIL: $1 -> $3"; fi
}
harness_report() {
  echo "----"
  echo "$pass passed, $fail failed"
  return $((fail > 0))
}

# prefixes reads a search response on stdin and prints the sorted set of
# distinct /u/<id> subtrees its record ids live under.
prefixes() {
  python3 -c '
import sys, json
raw = sys.stdin.read()
body = raw.split("HTTPSTATUS:")[0]
try:
    data = json.loads(body).get("data", [])
except json.JSONDecodeError:
    sys.exit(0)
seen = sorted({"/".join(r["id"].split("/")[:3]) for r in data})
print(" ".join(seen))
'
}
