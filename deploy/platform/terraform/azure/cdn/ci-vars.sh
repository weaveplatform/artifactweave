# Sourced by deploy/ci/scenario-ci.sh for a scenario run
# (platform-scenarios.yml): the inputs this root takes when the deployer
# applies it in CI. Everything lands in the bootstrap's scenarios resource
# group, where the deployer's rights end. The deployer is also the app
# identity: it runs artifactweave in the same job, so the account's public
# endpoint answers the runner's address alone (checkip reports it); a runner
# in the account's own region would be refused, since Azure applies IP rules
# only to requests from other regions, and test/platform's readiness check
# then fails. Its object ID comes from the oid claim of its own access token,
# as in azure/single-bucket. The endpoint's own *.azurefd.net host serves: a
# custom domain's managed certificate can take an hour, at the Premium
# profile's hourly fee. The location is the lab's choice.
# shellcheck shell=bash

export TF_VAR_subscription_id="$PLATFORM_AZURE_SUBSCRIPTION_ID"
export TF_VAR_name="${NAME_PREFIX}${INSTANCE}"
export TF_VAR_location=uksouth
export TF_VAR_resource_group_name="$PLATFORM_AZURE_RESOURCE_GROUP"
# export hides a failure here, but an empty ID fails app_principal_id's
# validation at plan, and an empty address the storage module's, before
# anything is created.
# shellcheck disable=SC2155
export TF_VAR_app_principal_id="$(az account get-access-token --query accessToken --output tsv | python3 -I -c 'import base64, json, re, sys; p = sys.stdin.read().strip().split(".")[1]; o = json.loads(base64.urlsafe_b64decode(p + "=" * (-len(p) % 4))).get("oid", ""); print(o) if re.fullmatch("[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}", o) else sys.exit("the access token carries no object ID (oid)")')"
# shellcheck disable=SC2155
export TF_VAR_app_ip_ranges="[\"$(curl -fsS --retry 3 https://checkip.amazonaws.com | tr -d '[:space:]')\"]"
export TF_VAR_tags="$SCENARIO_LABELS"
