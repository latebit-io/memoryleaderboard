#!/bin/bash
# Starts an isolated local backend and adapter, then runs one eval mode.
set -eo pipefail

# Resolved relative to this script at runtime.
# shellcheck disable=SC1091
source "$(dirname "$0")/lib/harness.sh"

mode=${1:-conformance}
if [ "$#" -gt 0 ]; then
  shift
fi

export ADAPTER_NAV=${ADAPTER_NAV:-off}
export ADAPTER_DISTILL=${ADAPTER_DISTILL:-off}
harness_start local-eval local-eval-key 180

"$EVAL" \
  --base-url "http://$ADDR" \
  --api-key "$API_KEY" \
  --timeout 180 \
  "$mode" "$@"
