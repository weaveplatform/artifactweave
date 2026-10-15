# Sourced by deploy/ci/scenario-ci.sh for a scenario run
# (platform-scenarios.yml): the inputs this root takes when the deployer
# applies it in CI. The deployer is also the app service account: it runs
# artifactweave in the same job. NAME_PREFIX is the start reap knows
# scenario names by. The location is the lab's choice.
# shellcheck shell=bash

export TF_VAR_project_id="$PLATFORM_GCP_PROJECT"
export TF_VAR_name="${NAME_PREFIX}${INSTANCE}"
export TF_VAR_location=europe-west2
export TF_VAR_app_service_account="$PLATFORM_GCP_SERVICE_ACCOUNT"
export TF_VAR_labels="$SCENARIO_LABELS"
export TF_VAR_force_destroy=true
