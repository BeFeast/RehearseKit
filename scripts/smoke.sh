#!/usr/bin/env bash

set -euo pipefail

BACKEND_URL="${BACKEND_URL:-http://localhost:8000}"
FRONTEND_URL="${FRONTEND_URL:-http://localhost:3000}"
WEBSOCKET_URL="${WEBSOCKET_URL:-http://localhost:8001}"

curl_check() {
  local name="$1"
  local url="$2"

  if ! curl \
    --fail \
    --silent \
    --retry 20 \
    --retry-all-errors \
    --retry-delay 2 \
    --max-time 5 \
    --output /dev/null \
    "$url"; then
    printf 'ERROR: %s did not respond (%s)\n' "$name" "$url" >&2
    return 1
  fi
  printf 'OK: %s (%s)\n' "$name" "$url"
}

if ! backend_health="$({
  curl \
    --fail \
    --silent \
    --retry 20 \
    --retry-all-errors \
    --retry-delay 2 \
    --max-time 5 \
    "${BACKEND_URL}/api/health"
})"; then
  printf 'ERROR: backend health endpoint did not respond\n' >&2
  exit 1
fi

case "$backend_health" in
  *'"status":"healthy"'*)
    printf 'OK: backend health (%s/api/health)\n' "$BACKEND_URL"
    ;;
  *)
    printf 'ERROR: backend dependencies are not healthy\n' >&2
    exit 1
    ;;
esac

curl_check "backend docs" "${BACKEND_URL}/docs"
curl_check "frontend" "$FRONTEND_URL"
curl_check "websocket health" "${WEBSOCKET_URL}/health"

printf 'RehearseKit smoke checks passed.\n'
