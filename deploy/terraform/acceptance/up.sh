#!/usr/bin/env bash
# Starts artifactweave in a local container against the acceptance bucket.
set -euo pipefail
cd "$(dirname "$0")"
./credentials.sh
docker compose up --build --detach
echo "artifactweave is starting on http://localhost:8081 (admin password in $(pwd)/.local/env)"
