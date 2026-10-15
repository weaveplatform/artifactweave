# Sourced by ../../scenario-ci.sh for a scenario run (platform-scenarios.yml):
# the variables this root takes when the deployer applies it in CI. As in
# azure/single-bucket, everything lands in the bootstrap's scenarios resource
# group, no conformance identity is looked up, no provider is registered,
# and the job runs artifactweave as the deployer.
# shellcheck shell=bash

export TF_VAR_subscription_id="$PLATFORM_AZURE_SUBSCRIPTION_ID"
export TF_VAR_name="awscn-$INSTANCE"
export TF_VAR_resource_group_name="$PLATFORM_AZURE_RESOURCE_GROUP"
export TF_VAR_conformance_identity=""
export TF_VAR_register_resource_providers=false
export TF_VAR_scenario_labels="$SCENARIO_LABELS"
