# Sourced by ../../scenario-ci.sh for a scenario run (platform-scenarios.yml):
# the variables this root takes when the deployer applies it in CI. As in
# aws/single-bucket, the run names the deployer as the app role, names the
# buckets after the instance inside the artifactweave- prefix the deployer
# may manage, and passes the tags reap matches. The replica region defaults
# to eu-west-1.
# shellcheck shell=bash

export TF_VAR_region="$PLATFORM_AWS_REGION"
export TF_VAR_name="artifactweave-awscn-$INSTANCE"
export TF_VAR_app_role_arn="$PLATFORM_AWS_ROLE_ARN"
export TF_VAR_scenario_labels="$SCENARIO_LABELS"
