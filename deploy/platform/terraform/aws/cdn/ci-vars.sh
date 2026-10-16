# Sourced by deploy/ci/scenario-ci.sh for a scenario run
# (platform-scenarios.yml): the inputs this root takes when the deployer
# applies it in CI. The deployer is also the app role: it runs artifactweave
# in the same job. NAME_PREFIX keeps the bucket inside the artifactweave-
# names the deployer may manage. scenario-ci.sh generates a fresh CloudFront
# key pair for each instance outside Terraform, so only the public half
# reaches the state, and resolves the name of the bootstrap's zone, where
# each instance gets its own sibling host, cdn-<instance>; reap finds a lost
# instance's records by that name.
# shellcheck shell=bash

export TF_VAR_name="${NAME_PREFIX}${INSTANCE}"
export TF_VAR_region="$PLATFORM_AWS_REGION"
export TF_VAR_app_role_arn="$PLATFORM_AWS_ROLE_ARN"
export TF_VAR_cloudfront_public_key="$CLOUDFRONT_PUBLIC_KEY"
export TF_VAR_cloudfront_private_key_file="$CLOUDFRONT_PRIVATE_KEY_FILE"
export TF_VAR_zone_id="$PLATFORM_AWS_DNS_ZONE_ID"
export TF_VAR_domain_name="cdn-${INSTANCE}.${PLATFORM_AWS_DNS_ZONE_NAME}"
export TF_VAR_tags="$SCENARIO_LABELS"
export TF_VAR_force_destroy=true
