#!/usr/bin/env bash
# Shared by the aws/ roots. Refuses to continue unless the AWS CLI is signed
# in to the lab account, recognised by the Route 53 zone the bootstrap
# adopts: aws.artifact.weaveplatform.run, delegated from GoDaddy. Checking the
# zone rather than an account ID keeps the account ID out of this public
# repository, and stops a login aimed at another account from being used.
set -euo pipefail
zone=${1:?usage: check-account.sh <hosted zone id>}
aws sts get-caller-identity >/dev/null 2>&1 || {
  echo "error: the AWS CLI is not signed in, or its session has expired; run aws login" >&2
  exit 1
}
name=$(aws route53 get-hosted-zone --id "$zone" --query HostedZone.Name --output text 2>/dev/null) || {
  echo "error: hosted zone $zone is not in the account the AWS CLI is signed in to" >&2
  exit 1
}
if [ "$name" != "aws.artifact.weaveplatform.run." ]; then
  echo "error: hosted zone $zone is $name, not aws.artifact.weaveplatform.run." >&2
  exit 1
fi
