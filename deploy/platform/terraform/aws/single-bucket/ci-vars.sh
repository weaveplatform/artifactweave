# Sourced by ../../scenario-ci.sh for a scenario run (platform-scenarios.yml):
# the variables this root takes when the deployer applies it in CI rather
# than an operator on a workstation. The run names the deployer as the app
# role (it runs artifactweave in the same job, and the boundary keeps it from
# reading the bootstrap's roles), names the bucket after the instance inside
# the artifactweave- prefix the deployer may manage, and passes the tags reap
# matches.
# shellcheck shell=bash

export TF_VAR_region="$PLATFORM_AWS_REGION"
export TF_VAR_name="artifactweave-awscn-$INSTANCE"
export TF_VAR_app_role_arn="$PLATFORM_AWS_ROLE_ARN"
export TF_VAR_scenario_labels="$SCENARIO_LABELS"
