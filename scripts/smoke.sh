#!/bin/bash
set -e
SCRATCH=$(mktemp -d)
REPO=$(cd "$(dirname "$0")/.." && pwd)
PORT=7311
ADDR=127.0.0.1:9311

# stale listeners from previous runs poison the port with dead tokens
lsof -tnP -iTCP:9311 2>/dev/null | xargs kill 2>/dev/null || true
pkill -f "demarkus-server.*smoke-root" 2>/dev/null || true
sleep 0.3

rm -rf "$SCRATCH/smoke-root" "$SCRATCH/tokens.toml"; mkdir -p "$SCRATCH/smoke-root"
TOKEN=$(~/.demarkus/bin/demarkus-token generate -label smoke -ops read,publish,lookup,list -paths '/**' -tokens "$SCRATCH/tokens.toml" 2>/dev/null | tail -1)
cleanup() { kill $SERVER_PID $ADAPTER_PID 2>/dev/null; wait 2>/dev/null; }
trap cleanup EXIT

~/.demarkus/bin/demarkus-server -root "$SCRATCH/smoke-root" -port $PORT -tokens "$SCRATCH/tokens.toml" >"$SCRATCH/server.log" 2>&1 &
SERVER_PID=$!

cd "$REPO" && go build -o "$SCRATCH/adapter" ./cmd/adapter
ADAPTER_ADDR=$ADDR DEMARKUS_HOST=localhost:$PORT DEMARKUS_INSECURE=1 DEMARKUS_TOKEN="$TOKEN" ADAPTER_API_KEY=smoke-key \
  "$SCRATCH/adapter" >"$SCRATCH/adapter.log" 2>&1 &
ADAPTER_PID=$!
for i in $(seq 1 20); do curl -sf http://$ADDR/healthz >/dev/null 2>&1 && break; sleep 0.5; done

pass=0; fail=0
check() { # name, expected-substring, actual
  if [[ "$3" == *"$2"* ]]; then echo "PASS: $1"; pass=$((pass+1)); else echo "FAIL: $1 -> $3"; fail=$((fail+1)); fi
}

R=$(curl -s -X POST http://$ADDR/add -H 'Content-Type: application/json' -H 'X-Api-Key: smoke-key' -d '{
  "request_id": "req-001", "user_id": "user-a", "session_id": "sess-1",
  "messages": [
    {"role": "user", "content": "The build for the parser project uses make lint before every commit", "timestamp": "2026-08-13T10:00:00Z"},
    {"role": "assistant", "content": "Noted: parser project requires make lint pre-commit."}
  ]}')
check "add echoes success" '"success":true' "$R"

R=$(curl -s -X POST http://$ADDR/add -H 'Content-Type: application/json' -H 'X-Api-Key: smoke-key' -d '{
  "request_id": "req-002", "user_id": "user-b", "session_id": "sess-9",
  "messages": [{"role": "user", "content": "My deploy workflow uses terraform apply"}]}')
check "add second user" '"success":true' "$R"

R=$(curl -s -X POST http://$ADDR/search -H 'Content-Type: application/json' -H 'X-Api-Key: smoke-key' -d '{
  "query": "parser lint", "user_id": "user-a", "top_k": 5}')
check "search finds evidence" 'parser' "$R"

R=$(curl -s -X POST http://$ADDR/search -H 'Content-Type: application/json' -H 'X-Api-Key: smoke-key' -d '{
  "query": "terraform deploy", "user_id": "user-a", "top_k": 5}')
check "isolation: user-a cannot see user-b" '"data":[]' "$R"

R=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://$ADDR/search -H 'Content-Type: application/json' -d '{"query":"x","user_id":"u"}')
check "missing auth is 401" '401' "$R"

echo "----"
echo "$pass passed, $fail failed"
exit $fail
