#!/bin/bash
set -euo pipefail

# Sign a darwin Mach-O binary with Developer ID or ad-hoc fallback.
# Usage: sign-darwin.sh <path-to-binary>

BINARY="$1"

if [ -z "${APPLE_SIGNING_IDENTITY:-}" ]; then
  echo "APPLE_SIGNING_IDENTITY not set, using ad-hoc signing..."
  codesign -s - -f "$BINARY"
else
  echo "Signing $BINARY with: $APPLE_SIGNING_IDENTITY"
  codesign -s "$APPLE_SIGNING_IDENTITY" \
    --timestamp \
    --options runtime \
    -f "$BINARY"
fi
