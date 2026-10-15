# Sourced by deploy/ci/scenario-ci.sh for a scenario run
# (platform-scenarios.yml): the inputs this root takes when the deployer
# applies it in CI. Everything lands in the bootstrap's scenarios resource
# group, where the deployer's rights end. The deployer is also the app
# identity: it runs artifactweave in the same job. It cannot read the
# directory, so its object ID comes from the oid claim of its own access
# token, checked as a GUID. NAME_PREFIX is the start reap knows scenario
# names by. The location is the lab's choice.
# shellcheck shell=bash

export TF_VAR_subscription_id="$PLATFORM_AZURE_SUBSCRIPTION_ID"
export TF_VAR_name="${NAME_PREFIX}${INSTANCE}"
export TF_VAR_location=uksouth
export TF_VAR_resource_group_name="$PLATFORM_AZURE_RESOURCE_GROUP"
# export hides a failure here, but an empty ID fails app_principal_id's
# validation at plan, before anything is created.
# shellcheck disable=SC2155
export TF_VAR_app_principal_id="$(az account get-access-token --query accessToken --output tsv | python3 -I -c 'import base64, json, re, sys; p = sys.stdin.read().strip().split(".")[1]; o = json.loads(base64.urlsafe_b64decode(p + "=" * (-len(p) % 4))).get("oid", ""); print(o) if re.fullmatch("[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}", o) else sys.exit("the access token carries no object ID (oid)")')"
export TF_VAR_tags="$SCENARIO_LABELS"
