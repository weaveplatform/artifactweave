#!/usr/bin/env bash
# Renders what the container needs from the Terraform outputs into .local/
# (git-ignored): its configuration, and an env file with the master key, the
# admin password and short-lived AWS credentials exported from your own
# `aws login` session. The master key and admin password are generated once
# and kept, because the database is sealed with that key; the AWS
# credentials are refreshed on every run, because they expire.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p .local
bucket=$(terraform output -raw bucket)
region=$(terraform output -raw region)
prefix=$(terraform output -raw store_prefix)
umask 077
if [ ! -f .local/secrets.env ]; then
  {
    echo "ARTIFACTWEAVE_MASTER_KEY=v1:$(openssl rand -base64 32)"
    echo "ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD=$(openssl rand -base64 18 | tr -d '/+=')"
  } > .local/secrets.env
fi
aws configure export-credentials --format env-no-export > .local/aws.env || {
  echo "run: aws login" >&2
  exit 1
}
cat .local/secrets.env .local/aws.env > .local/env
cat > .local/artifactweave.yaml <<YAML
listen: ":8081"
data_dir: /data
blob:
  backend: s3
  s3:
    bucket: ${bucket}
    region: ${region}
    prefix: ${prefix}
YAML
