#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

run_e2e=false
run_gpu=false

usage() {
  cat <<'EOF'
Usage: ./scripts/verify.sh [--e2e] [--gpu]

Runs the lightweight backend and frontend checks used for local verification.

Options:
  --e2e  Also run the Playwright end-to-end suite.
  --gpu  Also run backend tests with the full GPU/audio dependency set.
  -h, --help  Show this help text.
EOF
}

while (($# > 0)); do
  case "$1" in
    --e2e)
      run_e2e=true
      ;;
    --gpu)
      run_gpu=true
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'ERROR: unknown option: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'ERROR: %s is required.\n' "$1" >&2
    exit 1
  fi
}

require_command uv
require_command bun

printf '\n==> Backend unit tests\n'
"${ROOT_DIR}/backend/scripts/run_tests.sh"

if "$run_gpu"; then
  printf '\n==> Backend tests with GPU/audio dependencies\n'
  (
    cd "${ROOT_DIR}/backend"
    uv run \
      --python 3.11 \
      --with-requirements requirements.txt \
      --with aiosqlite \
      --with 'httpx<0.28' \
      pytest tests/
  )
fi

printf '\n==> Frontend dependencies\n'
(
  cd "${ROOT_DIR}/frontend"
  bun install --frozen-lockfile
)

printf '\n==> Frontend lint\n'
(
  cd "${ROOT_DIR}/frontend"
  bun run lint
)

printf '\n==> Frontend unit tests\n'
(
  cd "${ROOT_DIR}/frontend"
  bun run test -- --runInBand
)

if "$run_e2e"; then
  printf '\n==> Frontend Playwright end-to-end tests\n'
  (
    cd "${ROOT_DIR}/frontend"
    bun run test:e2e
  )
fi

printf '\nAll requested verification checks passed.\n'
