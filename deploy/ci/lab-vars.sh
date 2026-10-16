# shellcheck shell=bash
# The inputs an operator run of a deploy/platform scenario passes besides its
# Makefile's TF_VARS (decision 0030). scenario.mk's plan, apply and destroy
# source this file in the shell that then runs terraform, so the TF_VAR_*
# it exports reach that terraform and nothing else:
#
#   . deploy/ci/lab-vars.sh aws|gcp|azure|multicloud
#
# multicloud exports all three clouds' inputs, whose names do not overlap.
#
# Each value is the lab's, resolved with the operator's own CLI login:
#
#   account scope     project_id (gcp) and subscription_id (azure) from
#                     lab.mk; AWS takes the account from the credentials
#   app principal     the bootstrap's conformance identity, which
#                     cloud-conformance.yml runs as too
#   operator (azure)  you, as additional_principals.operator: Azure has no
#                     counterpart to impersonating the app identity, so an
#                     artifactweave on your workstation runs as you
#   CloudFront key    (aws/cdn) a key pair made once in the scenario's
#                     .secrets/cloudfront/ by cloudfront-key.sh, kept across
#                     applies and make clean: cloudfront_public_key, and
#                     cloudfront_private_key_file for the local runner
#   Cloud CDN key     (gcp/cdn) a signed URL key made once in the scenario's
#                     .secrets/cloud-cdn/ by cloud-cdn-key.sh, which make
#                     signed-url-key adds after the first apply:
#                     cloud_cdn_key_file, and signed_url_key_added once the
#                     applied backend bucket holds the key
#   force_destroy     true: a lab instance holds test data only
#
# The lab's constants reach this file from lab.mk through the environment
# (scenario.mk exports them); the bootstraps' fixed identity names are
# written here. Terraform ignores a TF_VAR_* for an input a root does not
# declare, so a scenario not yet on the v2 contract takes what it declares.

lab_vars() {
  local caller partition account app operator
  case ${1:-} in
  aws)
    caller=$(aws sts get-caller-identity --query Arn --output text) || return 1
    # arn:<partition>:sts::<account>:assumed-role/...: the conformance role
    # is in the same partition and account, at the bootstrap's role path.
    partition=$(cut -d: -f2 <<<"$caller")
    account=$(cut -d: -f5 <<<"$caller")
    export TF_VAR_app_role_arn="arn:${partition}:iam::${account}:role/artifactweave/artifactweave-conformance"
    # Run from the scenario's directory (scenario.mk's recipes are).
    if grep -qs '^variable "cloudfront_public_key"' variables.tf; then
      "$(dirname "${BASH_SOURCE[0]}")/cloudfront-key.sh" .secrets/cloudfront || return 1
      TF_VAR_cloudfront_public_key=$(cat .secrets/cloudfront/cloudfront-public-key.pem)
      export TF_VAR_cloudfront_public_key
      export TF_VAR_cloudfront_private_key_file="$PWD/.secrets/cloudfront/cloudfront-private-key.pem"
    fi
    ;;
  gcp)
    : "${LAB_GCP_PROJECT:?lab-vars.sh needs LAB_GCP_PROJECT from lab.mk}"
    export TF_VAR_project_id="$LAB_GCP_PROJECT"
    export TF_VAR_app_service_account="artifactweave-conformance@$LAB_GCP_PROJECT.iam.gserviceaccount.com"
    # Run from the scenario's directory (scenario.mk's recipes are).
    if grep -qs '^variable "cloud_cdn_key_file"' variables.tf; then
      local keys backend name
      keys=$(dirname "${BASH_SOURCE[0]}")/cloud-cdn-key.sh
      "$keys" make .secrets/cloud-cdn || return 1
      export TF_VAR_cloud_cdn_key_file="$PWD/.secrets/cloud-cdn/cloud-cdn-key"
      # The fill account's grant follows the key (gcp/cdn's README).
      if backend=$(terraform output -raw backend_bucket 2>/dev/null) &&
        name=$(terraform output -raw signed_url_key_name 2>/dev/null) &&
        "$keys" has "$LAB_GCP_PROJECT" "$backend" "$name" 2>/dev/null; then
        export TF_VAR_signed_url_key_added=true
      fi
    fi
    ;;
  azure)
    : "${LAB_AZURE_SUBSCRIPTION:?lab-vars.sh needs LAB_AZURE_SUBSCRIPTION from lab.mk}"
    : "${LAB_AZURE_BOOTSTRAP_RG:?lab-vars.sh needs LAB_AZURE_BOOTSTRAP_RG from lab.mk}"
    app=$(az identity show --subscription "$LAB_AZURE_SUBSCRIPTION" --resource-group "$LAB_AZURE_BOOTSTRAP_RG" \
      --name artifactweave-conformance --query principalId --output tsv) || return 1
    operator=$(az ad signed-in-user show --query id --output tsv) || {
      echo "lab-vars.sh: az ad signed-in-user show failed; an operator run needs a user's az login" >&2
      return 1
    }
    export TF_VAR_subscription_id="$LAB_AZURE_SUBSCRIPTION"
    export TF_VAR_app_principal_id="$app"
    export TF_VAR_additional_principals="{ operator = { principal_id = \"$operator\", principal_type = \"User\" } }"
    ;;
  multicloud)
    lab_vars aws && lab_vars gcp && lab_vars azure || return 1
    ;;
  *)
    echo "usage: . lab-vars.sh aws|gcp|azure|multicloud" >&2
    return 1
    ;;
  esac
  export TF_VAR_force_destroy=true
}

lab_vars "$@"
