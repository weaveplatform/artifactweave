# gcp/single-bucket in the lab, the GCS acceptance environment: make plan,
# apply and destroy pass these and deploy/ci/lab-vars.sh's inputs (the lab's
# project, and the bootstrap's conformance service account as the app service
# account). The name, the local/ prefix and the conformance/ expiry are the
# environment's from before the v2 contract, so its state plans as moves
# (moved.tf). The README's "How this scenario is tested" lists them. Plan and
# apply need `gcloud auth application-default login`.

TF_VARS = -var name=artifactweave-acc -var location=europe-west2 -var store_prefix=local/ \
  -var expiring_prefixes='{ "conformance/" = 1 }'

CONFORMANCE_ENV = \
  ARTIFACTWEAVE_TEST_GCS_BUCKET=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw bucket) \
  ARTIFACTWEAVE_TEST_GCS_ACCESS_ID=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw service_account) \
  GOOGLE_APPLICATION_CREDENTIALS=$(CURDIR)/.local/gcp-credentials.json
CONFORMANCE_RUN := TestAccContractOnRealGCS|TestAccRealGCSProbes
CONFORMANCE_PKG := ./pkg/blob/gcs/
# The service account and provider variables come from deploy/ci/terraform/gcp.
GITHUB_VARS := \
  CONFORMANCE_GCS_BUCKET=bucket

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
