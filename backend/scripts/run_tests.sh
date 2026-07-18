#!/usr/bin/env bash

# Test runner script for RehearseKit backend
# This script runs the test suite with proper configuration

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

cd "$BACKEND_DIR"

if ! command -v uv >/dev/null 2>&1; then
    printf 'ERROR: uv is required. Install it from https://docs.astral.sh/uv/.\n' >&2
    exit 1
fi

# Set test environment variables
export TESTING=true
export DATABASE_URL="sqlite+aiosqlite:///:memory:"
export REDIS_URL="redis://localhost:6379/0"
export CELERY_BROKER_URL="$REDIS_URL"
export CELERY_RESULT_BACKEND="$REDIS_URL"
export JWT_SECRET_KEY="test-secret-key-for-testing-only"
export GOOGLE_CLIENT_ID="test-client-id"
export GOOGLE_CLIENT_SECRET="test-client-secret"

printf 'Running backend unit tests...\n'
uv run \
    --python 3.11 \
    --with-requirements requirements-test.txt \
    pytest --asyncio-mode=auto -m "not integration and not slow" tests/ "$@"
