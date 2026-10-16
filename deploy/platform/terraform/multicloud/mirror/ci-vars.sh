# Sourced by deploy/ci/scenario-ci.sh for a scenario run
# (platform-scenarios.yml): the inputs this root takes when the deployers
# apply it in CI. The job holds all three clouds' deployers, and each is also
# its cloud's app identity: they run artifactweave in the same job.
# NAME_PREFIX keeps the S3 bucket inside the artifactweave- names the AWS
# deployer may manage, and the storage account lands in the Azure scenarios
# resource group, where that deployer's rights end. The Azure deployer cannot
# read the directory, so its object ID comes from the oid claim of its own
# access token, checked as a GUID. The locations and the home cloud are the
# lab's choice.
# shellcheck shell=bash

export TF_VAR_project_id="$PLATFORM_GCP_PROJECT"
export TF_VAR_subscription_id="$PLATFORM_AZURE_SUBSCRIPTION_ID"
export TF_VAR_name="${NAME_PREFIX}${INSTANCE}"
export TF_VAR_home_cloud=gcp
export TF_VAR_aws_region="$PLATFORM_AWS_REGION"
export TF_VAR_gcp_location=europe-west2
export TF_VAR_azure_location=uksouth
export TF_VAR_resource_group_name="$PLATFORM_AZURE_RESOURCE_GROUP"
export TF_VAR_app_role_arn="$PLATFORM_AWS_ROLE_ARN"
export TF_VAR_app_service_account="$PLATFORM_GCP_SERVICE_ACCOUNT"
# export hides a failure here, but an empty ID fails app_principal_id's
# validation at plan, before anything is created.
# shellcheck disable=SC2155
export TF_VAR_app_principal_id="$(az account get-access-token --query accessToken --output tsv | python3 -I -c 'import base64, json, re, sys; p = sys.stdin.read().strip().split(".")[1]; o = json.loads(base64.urlsafe_b64decode(p + "=" * (-len(p) % 4))).get("oid", ""); print(o) if re.fullmatch("[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}", o) else sys.exit("the access token carries no object ID (oid)")')"
export TF_VAR_labels="$SCENARIO_LABELS"
export TF_VAR_force_destroy=true
