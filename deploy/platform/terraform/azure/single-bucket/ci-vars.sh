# Sourced by ../../scenario-ci.sh for a scenario run (platform-scenarios.yml):
# the variables this root takes when the deployer applies it in CI rather
# than an operator on a workstation. The deployer's rights end at the
# bootstrap's scenarios resource group, so everything lands there; it cannot
# read the bootstrap's group, so no conformance identity is looked up; it has
# no subscription-level rights, so no provider is registered. The job runs
# artifactweave as the deployer, which the operator assignment covers.
# shellcheck shell=bash

export TF_VAR_subscription_id="$PLATFORM_AZURE_SUBSCRIPTION_ID"
export TF_VAR_name="awscn-$INSTANCE"
export TF_VAR_resource_group_name="$PLATFORM_AZURE_RESOURCE_GROUP"
export TF_VAR_conformance_identity=""
export TF_VAR_register_resource_providers=false
export TF_VAR_scenario_labels="$SCENARIO_LABELS"
