# aws/single-bucket: an S3 bucket whose bucket policy grants the bootstrap's
# conformance role its objects (Terraform), artifactweave in a local container
# against the bucket, and the real-cloud conformance tests. Apply
# deploy/ci/terraform/aws first. Plan and apply need `aws login` and refuse
# unless that login's account holds the delegated zone.

CONFORMANCE_ENV = \
  ARTIFACTWEAVE_TEST_S3_BUCKET=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw bucket) \
  ARTIFACTWEAVE_TEST_S3_REGION=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw region)
CONFORMANCE_RUN := TestContractOnRealS3|TestRealS3Probes
CONFORMANCE_PKG := ./pkg/blob/s3/
# The role and region variables, and the environment, come from the
# bootstrap (deploy/ci/terraform/aws).
GITHUB_VARS := \
  CONFORMANCE_S3_BUCKET=bucket

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
