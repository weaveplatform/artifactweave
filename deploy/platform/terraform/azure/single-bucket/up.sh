#!/usr/bin/env bash
# Starts artifactweave in a local container against the acceptance account.
set -euo pipefail
cd "$(dirname "$0")"
# compose.yaml builds from the repository root; make exports it, a direct run finds it.
export ARTIFACTWEAVE_ROOT=${ARTIFACTWEAVE_ROOT:-$(git rev-parse --show-toplevel)}
./credentials.sh
docker compose up --build --detach
echo "artifactweave is starting on http://localhost:8082 (admin password in $(pwd)/.local/env)"
