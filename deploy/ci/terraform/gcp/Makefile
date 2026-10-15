# The GCP bootstrap (deploy/ci/terraform/gcp): the lab project's APIs,
# GitHub's trust in the conformance and deployer service accounts, the remote
# state bucket, the DNS zone and a budget. Plan and apply need
# `gcloud auth application-default login`. Run from the repository root with
# make ci-<target> CLOUD=gcp.

STATE_PROBE = gcloud storage buckets describe gs://artifactweave-tfstate-1025324209347 --project $(LAB_GCP_PROJECT)
GITHUB_VARS := \
  CONFORMANCE_GCP_SERVICE_ACCOUNT=conformance_service_account \
  CONFORMANCE_GCP_WIF_PROVIDER=workload_identity_provider \
  PLATFORM_GCP_SERVICE_ACCOUNT=deployer_service_account \
  PLATFORM_GCP_WIF_PROVIDER=workload_identity_provider \
  PLATFORM_GCP_PROJECT=project_id \
  PLATFORM_GCP_STATE_BUCKET=state_bucket

.PHONY: undelete-pool

## undelete-pool: restore the github-actions pool and its provider, deleted with the old acceptance root
undelete-pool:
	@state=$$(gcloud iam workload-identity-pools describe github-actions --location=global \
	  --project $(LAB_GCP_PROJECT) --format='value(state)'); \
	if [ "$$state" = DELETED ]; then \
	  gcloud iam workload-identity-pools undelete github-actions --location=global --project $(LAB_GCP_PROJECT); \
	else echo "pool github-actions is $$state"; fi
	@state=$$(gcloud iam workload-identity-pools providers describe artifactweave \
	  --workload-identity-pool=github-actions --location=global --project $(LAB_GCP_PROJECT) --format='value(state)'); \
	if [ "$$state" = DELETED ]; then \
	  gcloud iam workload-identity-pools providers undelete artifactweave \
	    --workload-identity-pool=github-actions --location=global --project $(LAB_GCP_PROJECT); \
	else echo "provider artifactweave is $$state"; fi

include $(shell git rev-parse --show-toplevel)/deploy/ci/bootstrap.mk
