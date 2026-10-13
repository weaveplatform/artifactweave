#!/usr/bin/env bash
# Round-trips an artifact through the running container: create a generic
# repository, deploy a 40 MiB file (more than two 16 MiB GCS parts, so the
# backend composes), download it and compare checksums.
set -euo pipefail
cd "$(dirname "$0")"
base=${BASE_URL:-http://localhost:8081/artifactory}
password=$(sed -n 's/^ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD=//p' .local/env)
auth=(-u "admin:${password}")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for _ in $(seq 1 30); do
  curl -fsS "${base}/api/system/ping" >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS "${auth[@]}" -X PUT -H 'Content-Type: application/json' \
  -d '{"rclass":"local","packageType":"generic"}' \
  "${base}/api/repositories/gcs-smoke" >/dev/null || true

head -c $((40 << 20)) /dev/urandom > "$work/big.bin"
sha=$(shasum -a 256 "$work/big.bin" | cut -d' ' -f1)
curl -fsS "${auth[@]}" -X PUT -H "X-Checksum-Sha256: ${sha}" \
  -T "$work/big.bin" "${base}/gcs-smoke/smoke/big.bin" >/dev/null
curl -fsS "${auth[@]}" -o "$work/back.bin" "${base}/gcs-smoke/smoke/big.bin"
back=$(shasum -a 256 "$work/back.bin" | cut -d' ' -f1)
[ "$sha" = "$back" ] || { echo "checksum mismatch: $sha vs $back" >&2; exit 1; }
echo "deployed and downloaded 40 MiB through GCS: sha256 $sha"
curl -fsS "${auth[@]}" "${base}/api/storageinfo" | python3 -m json.tool | sed -n '1,20p'
