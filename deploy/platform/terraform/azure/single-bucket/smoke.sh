#!/usr/bin/env bash
# Round-trips an artifact through the running container: create a generic
# repository, deploy a 40 MiB file (five 8 MiB blocks), download it, compare
# checksums, and read the committed block list of the stored blob.
set -euo pipefail
cd "$(dirname "$0")"
base=${BASE_URL:-http://localhost:8082/artifactory}
password=$(sed -n 's/^ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD=//p' .local/env)
auth=(-u "admin:${password}")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for _ in $(seq 1 30); do
  curl -fsS "${base}/api/system/ping" >/dev/null 2>&1 && break
  sleep 1
done
# Answers 400 when the repository exists from an earlier run.
curl -fs "${auth[@]}" -X PUT -H 'Content-Type: application/json' \
  -d '{"rclass":"local","packageType":"generic"}' \
  "${base}/api/repositories/azure-smoke" >/dev/null || true

head -c $((40 << 20)) /dev/urandom > "$work/big.bin"
sha=$(shasum -a 256 "$work/big.bin" | cut -d' ' -f1)
curl -fsS "${auth[@]}" -X PUT -H "X-Checksum-Sha256: ${sha}" \
  -T "$work/big.bin" "${base}/azure-smoke/smoke/big.bin" >/dev/null
curl -fsS "${auth[@]}" -o "$work/back.bin" "${base}/azure-smoke/smoke/big.bin"
back=$(shasum -a 256 "$work/back.bin" | cut -d' ' -f1)
[ "$sha" = "$back" ] || { echo "checksum mismatch: $sha vs $back" >&2; exit 1; }
echo "deployed and downloaded 40 MiB through Azure: sha256 $sha"

sub=$(terraform output -raw subscription_id)
account=$(terraform output -raw account)
container=$(terraform output -raw container)
prefix=$(terraform output -raw store_prefix)
token=$(az account get-access-token --subscription "$sub" \
  --resource https://storage.azure.com/ --query accessToken -o tsv)
blocks=$(curl -fsS -H "Authorization: Bearer ${token}" -H "x-ms-version: 2023-11-03" \
  "https://${account}.blob.core.windows.net/${container}/${prefix}sha256/${sha:0:2}/${sha}?comp=blocklist&blocklisttype=committed" |
  grep -o '<Block>' | wc -l | tr -d ' ')
echo "the stored blob has ${blocks} committed blocks"
curl -fsS "${auth[@]}" "${base}/api/storageinfo" | python3 -m json.tool | sed -n '1,20p'
