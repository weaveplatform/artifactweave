# Sourced by deploy/ci/scenario-ci.sh for a scenario run
# (platform-scenarios.yml): the inputs this root takes when the deployer
# applies it in CI. The deployer is also the app role: it runs artifactweave
# in the same job. NAME_PREFIX keeps every bucket inside the artifactweave-
# names the deployer may manage. The second region is the lab's choice, and
# must differ from PLATFORM_AWS_REGION.
# shellcheck shell=bash

export TF_VAR_name="${NAME_PREFIX}${INSTANCE}"
export TF_VAR_regions="[\"$PLATFORM_AWS_REGION\", \"eu-west-1\"]"
export TF_VAR_app_role_arn="$PLATFORM_AWS_ROLE_ARN"
export TF_VAR_tags="$SCENARIO_LABELS"
export TF_VAR_force_destroy=true
