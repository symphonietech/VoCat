#!/usr/bin/env bash
set -euo pipefail

# Build (and by default start) the local Docker image with a version derived
# from the current git state, instead of the Dockerfile's static
# "0.1.0-dev" fallback. This embeds a real version into the binary (`vocat
# version`, Settings > System Info, GET /api/system/info) and gives the
# image its own distinct tag, so a later `docker compose pull` cannot
# silently replace this build under the shared ":latest" tag.
#
# Usage:
#   scripts/docker-build.sh              # build image + docker compose up -d
#   scripts/docker-build.sh --build-only  # docker compose build, no restart
#
# Override VOCAT_TAG yourself (e.g. a release tag) to skip git derivation.

cd "$(dirname "$0")/.."

if [[ -z "${VOCAT_TAG:-}" ]]; then
  if git describe --tags --always --dirty >/dev/null 2>&1; then
    VOCAT_TAG="$(git describe --tags --always --dirty)"
  else
    VOCAT_TAG="unknown"
  fi
fi
export VOCAT_TAG

if [[ -z "${VOCAT_BUILD_TIME:-}" ]]; then
  VOCAT_BUILD_TIME="$(git show -s --format=%cI HEAD 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)"
fi
export VOCAT_BUILD_TIME

echo "Building vocat image: ghcr.io/mengmengcode/vocat:${VOCAT_TAG} (build time ${VOCAT_BUILD_TIME})"

if [[ "${1:-}" == "--build-only" ]]; then
  exec docker compose build
fi

exec docker compose up -d --build
