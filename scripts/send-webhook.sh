#!/usr/bin/env bash
# Send a signed Bitbucket-style repo:push webhook to a local API, for testing
# without Bitbucket.
#
#   scripts/send-webhook.sh [payload.json]
#
# Environment:
#   WEBHOOK_URL     default http://localhost:8080/webhooks/bitbucket
#   WEBHOOK_SECRET  default dev-secret (must equal the API's BITBUCKET_WEBHOOK_SECRET)
#   EVENT_KEY       default repo:push
#   FRESH           default 1: give every commit in the payload a new random hash so
#                   it counts as a new commit (commits are de-duplicated by hash).
#                   Set FRESH=0 to resend the file as it is.
#
# Needs: bash, curl, openssl.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
file="${1:-$root/backend/testdata/repo_push.json}"
url="${WEBHOOK_URL:-http://localhost:8080/webhooks/bitbucket}"
secret="${WEBHOOK_SECRET:-dev-secret}"
event="${EVENT_KEY:-repo:push}"

body="$(cat "$file")"
if [ "${FRESH:-1}" = "1" ]; then
  # Replace every 7+ hex-digit string used as a commit hash in the fixture with
  # a random one, consistently (the same old hash maps to the same new hash).
  for old in $(printf '%s' "$body" | grep -oE '"(hash|name)": *"[0-9a-f]{7,40}"' | grep -oE '[0-9a-f]{7,40}' | sort -u); do
    new="$(openssl rand -hex 20 | cut -c1-${#old})"
    body="${body//$old/$new}"
  done
fi

sig="sha256=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$secret" | awk '{print $NF}')"
uuid="$(openssl rand -hex 16)"

echo "POST $url  event=$event  request_uuid=$uuid"
curl -sS -i -X POST "$url" \
  -H "Content-Type: application/json" \
  -H "X-Event-Key: $event" \
  -H "X-Request-UUID: $uuid" \
  -H "X-Hub-Signature: $sig" \
  --data-binary "$body" | sed -n '1p;$p'
