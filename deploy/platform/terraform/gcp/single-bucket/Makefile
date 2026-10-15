# gcp/single-bucket: a GCS bucket granted to the bootstrap's conformance
# service account (Terraform), artifactweave run natively against it (make up,
# deploy/ci/local.sh), and the real-cloud conformance tests. Apply
# deploy/ci/terraform/gcp first. Plan and apply need
# `gcloud auth application-default login`.

CONFORMANCE_ENV = \
  ARTIFACTWEAVE_TEST_GCS_BUCKET=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw bucket) \
  ARTIFACTWEAVE_TEST_GCS_ACCESS_ID=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw service_account) \
  GOOGLE_APPLICATION_CREDENTIALS=$(CURDIR)/.local/gcp-credentials.json
CONFORMANCE_RUN := TestContractOnRealGCS|TestRealGCSProbes
CONFORMANCE_PKG := ./pkg/blob/gcs/
# The service account and provider variables come from deploy/ci/terraform/gcp.
GITHUB_VARS := \
  CONFORMANCE_GCS_BUCKET=bucket

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
