#!/bin/bash
# Live agentic-search check against a real demarkus server and the
# configured LLM. Skips when no provider is available.
set -eo pipefail
# Resolved relative to this script at runtime.
# shellcheck disable=SC1091
source "$(dirname "$0")/lib/harness.sh"

# require: no silent degradation to catalog lookup, so a nav failure
# surfaces as a failed assertion rather than a passing lookup result.
export ADAPTER_NAV=require
export ADAPTER_DISTILL=require
# An agentic search costs seconds; allow a full nav budget per request.
if ! harness_start nav k 180; then
  if grep -Eq 'ADAPTER_(NAV|DISTILL)=require but no LLM provider is configured' "$ADAPTER_LOG"; then
    echo "SKIP: no LLM provider configured; agentic search cannot run"
    exit 0
  fi
  exit 1
fi

if ! $CURL "http://$ADDR/healthz" | grep -q '"nav":true'; then
  echo "SKIP: no LLM provider configured; agentic search cannot run"
  exit 0
fi

add() { post add "$1" >/dev/null; }
add '{"request_id":"r1","user_id":"u","session_id":"s1","messages":[{"role":"user","content":"The parser project CI fails intermittently because the golden test fixtures assume UTC; we fixed it by pinning TZ=UTC in the Makefile test target","timestamp":1782896400000}]}'
add '{"request_id":"r2","user_id":"u","session_id":"s1","messages":[{"role":"user","content":"For deployments we use a single droplet with caddy in front, TLS via automatic ACME","timestamp":1782982800000}]}'
add '{"request_id":"r3","user_id":"u","session_id":"s2","messages":[{"role":"user","content":"Reminder: the flaky CI issue traced back to timezone handling in date formatting, not to network flakiness","timestamp":1783242000000}]}'

RESULT=$(post search '{"query":"why was continuous integration flaky and how was it fixed","user_id":"u","top_k":5}')
check "search returns 200" 'HTTPSTATUS:200' "$RESULT"

# Two CI documents are relevant, the deployment one is not. This gates on
# "found relevant evidence, returned no noise"; recall quality itself is
# what the phase-4 self-eval measures, not this smoke.
if echo "$RESULT" | python3 -c '
import sys, json
raw = sys.stdin.read().split("HTTPSTATUS:")[0]
data = json.loads(raw).get("data", [])
print(f"{len(data)} records")
for i, r in enumerate(data):
    body = r["content"]
    first = body.splitlines()[0] if body else ""
    doc_id, score = r["id"], r.get("score")
    print(f"  {i+1}. {doc_id} score={score} {first}")
relevant = sum(1 for r in data if "CI" in r["content"] or "flaky" in r["content"])
noise = sum(1 for r in data if "caddy" in r["content"])
print(f"relevant={relevant}/2 noise={noise}")
sys.exit(0 if relevant >= 1 and noise == 0 else 1)
'; then
  report "agentic search returns relevant evidence without noise" 1
else
  report "agentic search returns relevant evidence without noise" 0
fi

harness_report
