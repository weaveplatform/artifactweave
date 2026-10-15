# Sourced by deploy/ci/scenario-ci.sh for a scenario run
# (platform-scenarios.yml): the inputs this root takes when the deployer
# applies it in CI. The deployer is also the app role: it runs artifactweave
# in the same job. NAME_PREFIX keeps the buckets inside the artifactweave-
# names the deployer may manage. The deployer may create roles only under
# the bootstrap's scenario path and with its permissions boundary, which
# lives in the deployer's own account and path (deploy/ci/terraform/aws);
# the ARN is built from the role's secret, whose account the workflow masks.
# The replica region is the lab's choice, and must differ from
# PLATFORM_AWS_REGION.
# shellcheck shell=bash

export TF_VAR_name="${NAME_PREFIX}${INSTANCE}"
export TF_VAR_region="$PLATFORM_AWS_REGION"
export TF_VAR_replica_region=eu-west-1
export TF_VAR_app_role_arn="$PLATFORM_AWS_ROLE_ARN"
export TF_VAR_permissions_boundary_arn="${PLATFORM_AWS_ROLE_ARN%%:role/*}:policy/artifactweave/artifactweave-scenario-boundary"
export TF_VAR_iam_role_path=/artifactweave-scenarios/
export TF_VAR_tags="$SCENARIO_LABELS"
export TF_VAR_force_destroy=true
