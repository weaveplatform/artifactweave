# Sourced by ../../scenario-ci.sh for a scenario run (platform-scenarios.yml):
# the variables this root takes when the deployer applies it in CI. As in
# gcp/single-bucket, the run names the deployer as the app service account,
# names the bucket after the instance and passes the labels reap matches.
# shellcheck shell=bash

export TF_VAR_project_id="$PLATFORM_GCP_PROJECT"
export TF_VAR_name="awscn-$INSTANCE"
export TF_VAR_app_service_account="$PLATFORM_GCP_SERVICE_ACCOUNT"
export TF_VAR_scenario_labels="$SCENARIO_LABELS"
