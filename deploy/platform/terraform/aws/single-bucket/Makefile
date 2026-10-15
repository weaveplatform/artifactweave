# aws/single-bucket: an S3 bucket whose bucket policy grants the bootstrap's
# conformance role its objects (Terraform), artifactweave in a local
# container against the bucket, and the real-cloud conformance tests. Apply
# aws/bootstrap first. Plan and apply need `aws login` and refuse unless that
# login's account holds the delegated zone.

ZONE_ID ?= Z06607493A97HOZP218UN
GUARD := check-account
CONFORMANCE_DEPS := $(GUARD)
CONFORMANCE_ENV = \
  ARTIFACTWEAVE_TEST_S3_BUCKET=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw bucket) \
  ARTIFACTWEAVE_TEST_S3_REGION=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw region)
CONFORMANCE_RUN := TestContractOnRealS3|TestRealS3Probes
CONFORMANCE_PKG := ./pkg/blob/s3/
# The role and region variables, and the environment, come from aws/bootstrap.
GITHUB_VARS := \
  CONFORMANCE_S3_BUCKET=bucket

.PHONY: check-account

## check-account: refuse unless the AWS CLI is signed in to the account holding the delegated zone
check-account:
	@../check-account.sh $(ZONE_ID)

include $(shell git rev-parse --show-toplevel)/deploy/platform/terraform/scenario.mk
