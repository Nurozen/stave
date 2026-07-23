#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MARMOT_REPO="${STAVE_E2E_MARMOT_REPO:-$(dirname "$ROOT")/context-marmot}"
TMP="$ROOT/test_rig/tmp/memory-e2e"

if [[ ! -d "$MARMOT_REPO" ]]; then
  printf 'ERROR: context-marmot repo not found at %s (set STAVE_E2E_MARMOT_REPO).\n' "$MARMOT_REPO" >&2
  exit 1
fi

rm -rf "$TMP"
mkdir -p "$TMP"

printf '### build marmot HEAD from %s\n' "$MARMOT_REPO"
(cd "$MARMOT_REPO" && go build -o "$TMP/marmot" ./cmd/marmot)

export STAVE_E2E_MARMOT="$TMP/marmot"

printf '### run cross-repo memory e2e\n'
cd "$ROOT"
go test -count=1 -run TestMarmotMemoryE2E -v ./internal/cli/
