# Sourced by ../../scenario-ci.sh for a scenario run (platform-scenarios.yml):
# the variables this root takes when the deployer applies it in CI. As in
# gcp/single-bucket: the deployer is the app service account (it runs
# artifactweave in the same job, and cannot read service accounts to look up
# the conformance one), the buckets are named after the instance, and they
# carry the labels reap matches.
# shellcheck shell=bash

export TF_VAR_project_id="$PLATFORM_GCP_PROJECT"
export TF_VAR_name="awscn-$INSTANCE"
export TF_VAR_app_service_account="$PLATFORM_GCP_SERVICE_ACCOUNT"
export TF_VAR_scenario_labels="$SCENARIO_LABELS"
