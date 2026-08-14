#!/bin/bash
# End-to-end contract smoke: Add/Search shape, user isolation, auth.
set -eo pipefail
source "$(dirname "$0")/lib/harness.sh"

# Contract shape only: catalog lookup keeps this deterministic and fast.
# scripts/nav-smoke.sh covers the agentic path.
export ADAPTER_NAV=off
harness_start smoke smoke-key

R=$(post add '{
  "request_id": "req-001", "user_id": "user-a", "session_id": "sess-1",
  "messages": [
    {"role": "user", "content": "The build for the parser project uses make lint before every commit", "timestamp": "2026-08-13T10:00:00Z"},
    {"role": "assistant", "content": "Noted: parser project requires make lint pre-commit."}
  ]}')
check "add returns 200" 'HTTPSTATUS:200' "$R"
check "add echoes success" '"success":true' "$R"

R=$(post add '{
  "request_id": "req-002", "user_id": "user-b", "session_id": "sess-9",
  "messages": [{"role": "user", "content": "My deploy workflow uses terraform apply"}]}')
check "add second user" '"success":true' "$R"

R=$(post search '{"query": "parser lint", "user_id": "user-a", "top_k": 5}')
check "search returns 200" 'HTTPSTATUS:200' "$R"
check "search finds evidence" 'parser' "$R"

# Isolation is a property of the returned paths: every record must live
# under one /u/<id>/ subtree. Asserting on paths holds for any search
# strategy, including an agentic one that returns in-scope documents for
# an off-topic query.
A_PREFIXES=$(post search '{"query": "parser lint", "user_id": "user-a", "top_k": 5}' | prefixes)
B_PREFIXES=$(post search '{"query": "terraform deploy", "user_id": "user-b", "top_k": 5}' | prefixes)
CROSS=$(post search '{"query": "terraform deploy apply workflow", "user_id": "user-a", "top_k": 5}' | prefixes)
if [ -n "$A_PREFIXES" ] && [ -n "$B_PREFIXES" ] && [ "$A_PREFIXES" != "$B_PREFIXES" ]; then
  report "users occupy distinct subtrees" 1
else
  report "users occupy distinct subtrees" 0
  echo "FAIL: user-a=$A_PREFIXES user-b=$B_PREFIXES"
fi
if [ -z "$CROSS" ] || [ "$CROSS" = "$A_PREFIXES" ]; then
  report "isolation: user-a never sees user-b's subtree" 1
else
  report "isolation: user-a never sees user-b's subtree" 0
  echo "FAIL: user-a search returned $CROSS"
fi

R=$($CURL -o /dev/null -w '%{http_code}' -X POST "http://$ADDR/search" -H 'Content-Type: application/json' -d '{"query":"x","user_id":"u"}')
check "missing auth is 401" '401' "$R"

harness_report
