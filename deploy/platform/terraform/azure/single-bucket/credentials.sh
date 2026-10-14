#!/usr/bin/env bash
# Renders what the local container needs from the Terraform outputs into
# .local/ (git-ignored): its configuration and an env file with the master
# key, the admin password and a container-scoped user delegation SAS minted
# from your Entra ID login. The storage account allows no Shared Key, and
# DefaultAzureCredential cannot reach the host's Azure CLI from inside the
# image, so the container runs on that SAS. The master key and password are
# kept across runs (the database is sealed with the key); the SAS is minted
# afresh each time and expires within seven days, the user delegation limit.
set -euo pipefail
cd "$(dirname "$0")"
sub=$(terraform output -raw subscription_id)
../check-subscription.sh "$sub"
account=$(terraform output -raw account)
container=$(terraform output -raw container)
prefix=$(terraform output -raw store_prefix)

mkdir -p .local
umask 077
if [ -f .local/env ]; then
  master=$(sed -n 's/^ARTIFACTWEAVE_MASTER_KEY=//p' .local/env)
  password=$(sed -n 's/^ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD=//p' .local/env)
else
  master="v1:$(openssl rand -base64 32)"
  password=$(openssl rand -base64 18 | tr -d '/+=')
fi
# Just under the seven-day maximum, so clock skew cannot push it over.
expiry=$(date -u -v+6d -v+23H +%Y-%m-%dT%H:%MZ 2>/dev/null ||
  date -u -d '+6 days 23 hours' +%Y-%m-%dT%H:%MZ)
token=$(az storage container generate-sas --subscription "$sub" \
  --account-name "$account" --name "$container" --permissions racwdl \
  --expiry "$expiry" --https-only --auth-mode login --as-user -o tsv)
{
  echo "ARTIFACTWEAVE_MASTER_KEY=${master}"
  echo "ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD=${password}"
  echo "ARTIFACTWEAVE_BLOB_AZURE_SAS_TOKEN=${token}"
} > .local/env
cat > .local/artifactweave.yaml <<YAML
listen: ":8081"
data_dir: /data
blob:
  backend: azure
  azure:
    account: ${account}
    container: ${container}
    prefix: ${prefix}
    # The token is ARTIFACTWEAVE_BLOB_AZURE_SAS_TOKEN in .local/env.
    credential: sas
YAML
echo "wrote .local/ (the SAS expires ${expiry})"
