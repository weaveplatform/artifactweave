#!/usr/bin/env bash
# Renders what the container and the conformance tests need from the
# Terraform outputs: impersonated credentials in .secrets/ and the container's
# configuration and secrets in .local/ (both git-ignored). The master key and
# admin password are generated once and kept, because the database is sealed
# with that key.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p .local
bucket=$(terraform output -raw bucket)
prefix=$(terraform output -raw store_prefix)
account=$(terraform output -raw service_account)

# The container acts as the service account by impersonation from your own
# Application Default Credentials: the organisation forbids service account
# keys. This is the file `gcloud auth application-default login
# --impersonate-service-account` would write, without replacing your ADC.
adc=${CLOUDSDK_CONFIG:-$HOME/.config/gcloud}/application_default_credentials.json
[ -f "$adc" ] || { echo "run: gcloud auth application-default login" >&2; exit 1; }
mkdir -p .secrets
umask 077
python3 -I - "$adc" "$account" > .secrets/container-credentials.json <<'PY'
import json, sys
source, account = sys.argv[1], sys.argv[2]
json.dump({
    "type": "impersonated_service_account",
    "service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/"
    f"projects/-/serviceAccounts/{account}:generateAccessToken",
    "delegates": [],
    "source_credentials": json.load(open(source)),
}, sys.stdout)
PY
if [ ! -f .local/env ]; then
  {
    echo "ARTIFACTWEAVE_MASTER_KEY=v1:$(openssl rand -base64 32)"
    echo "ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD=$(openssl rand -base64 18 | tr -d '/+=')"
  } > .local/env
fi
cat > .local/artifactweave.yaml <<YAML
listen: ":8081"
data_dir: /data
blob:
  backend: gcs
  gcs:
    bucket: ${bucket}
    prefix: ${prefix}
    # Signed download URLs go through IAM signBlob as this account.
    google_access_id: ${account}
YAML
