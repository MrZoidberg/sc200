#!/usr/bin/env bash
# Verify the public site without executing downloaded code.
set -euo pipefail
base=${1:-https://sc200.merkulov.me}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
for attempt in 1 2 3 4 5; do
  if curl -fsS --max-time 20 "$base/" -o "$work/index" &&
     curl -fsS --max-time 20 -D "$work/headers" "$base/install.sh" -o "$work/install" &&
     cmp scripts/install.sh "$work/install" &&
     grep -qi 'content-type: text/plain' "$work/headers" &&
     grep -qi 'cache-control:.*no-store' "$work/headers" &&
     grep -qi 'x-content-type-options: nosniff' "$work/headers" &&
     grep -q "${WEBSITE_VERSION:-development}" "$work/index"; then
    exit 0
  fi
  sleep 5
done
exit 1
