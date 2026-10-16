# Sourced by deploy/ci/scenario-ci.sh for a scenario run
# (platform-scenarios.yml): the inputs this root takes when the deployer
# applies it in CI. The deployer is also the app service account: it runs
# artifactweave in the same job. NAME_PREFIX is the start reap knows the
# load balancer's unlabelled pieces by. scenario-ci.sh generates a fresh
# signed URL key for each instance outside Terraform, adds it with gcloud
# after the first apply and applies again with signed_url_key_added, and
# resolves the bootstrap zone's name, where each instance gets its own
# sibling host, cdn-<instance>; reap finds a lost instance's records by
# that name. The location is the lab's choice.
# shellcheck shell=bash

export TF_VAR_project_id="$PLATFORM_GCP_PROJECT"
export TF_VAR_name="${NAME_PREFIX}${INSTANCE}"
export TF_VAR_location=europe-west2
export TF_VAR_app_service_account="$PLATFORM_GCP_SERVICE_ACCOUNT"
export TF_VAR_cloud_cdn_key_file="$CLOUD_CDN_KEY_FILE"
export TF_VAR_dns_zone="$PLATFORM_GCP_DNS_ZONE"
export TF_VAR_domain_name="cdn-${INSTANCE}.${PLATFORM_GCP_DNS_NAME}"
export TF_VAR_labels="$SCENARIO_LABELS"
export TF_VAR_force_destroy=true
