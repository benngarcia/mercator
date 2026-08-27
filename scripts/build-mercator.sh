#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT="${1:?usage: scripts/build-mercator.sh OUTPUT}"
REVISION="${MERCATOR_BUILD_REVISION:?MERCATOR_BUILD_REVISION is required}"
EXTRA_LDFLAGS="${MERCATOR_BUILD_LDFLAGS:-}"

cd "${ROOT}"
CGO_ENABLED="${CGO_ENABLED:-0}" go build -trimpath \
  -ldflags="${EXTRA_LDFLAGS} -X github.com/benngarcia/mercator/internal/httpapi.buildRevisionOverride=${REVISION}" \
  -o "${OUTPUT}" ./cmd/mercator
