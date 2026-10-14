#!/usr/bin/env bash
# Refuses to continue unless the Azure CLI's active subscription is the one
# given and its tenant owns that subscription. Terraform pins the
# subscription as well; this stops a login aimed at another subscription
# from being used for anything, including the az commands the scripts run.
set -euo pipefail
want=${1:?usage: check-subscription.sh <subscription id>}
active=$(az account show --query id -o tsv 2>/dev/null) || {
  echo "error: the Azure CLI is not logged in; run az login" >&2
  exit 1
}
if [ "$active" != "$want" ]; then
  echo "error: the active Azure subscription is $active, not $want." >&2
  echo "       Run: az account set --subscription $want" >&2
  exit 1
fi
owner=$(az account show --subscription "$want" --query tenantId -o tsv)
tenant=$(az account show --query tenantId -o tsv)
if [ "$owner" != "$tenant" ]; then
  echo "error: the login's tenant $tenant does not own subscription $want (tenant $owner)" >&2
  exit 1
fi
