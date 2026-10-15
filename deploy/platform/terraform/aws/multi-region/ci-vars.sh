# Sourced by ../../scenario-ci.sh for a scenario run (platform-scenarios.yml):
# the variables this root takes when the deployer applies it in CI. As in
# aws/single-bucket: the deployer is the app role (it runs artifactweave in
# the same job, and the boundary keeps it from reading the bootstrap's
# roles), the buckets are named after the instance inside the artifactweave-
# prefix the deployer may manage, and they carry the tags reap matches. The
# primary region is the bootstrap's; the secondary keeps its default.
# shellcheck shell=bash

export TF_VAR_primary_region="$PLATFORM_AWS_REGION"
export TF_VAR_name="artifactweave-awscn-$INSTANCE"
export TF_VAR_app_role_arn="$PLATFORM_AWS_ROLE_ARN"
export TF_VAR_scenario_labels="$SCENARIO_LABELS"
