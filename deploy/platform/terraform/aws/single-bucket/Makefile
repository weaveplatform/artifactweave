# aws/single-bucket in the lab: the S3 acceptance environment, which a local
# run (make up, deploy/ci/local.sh) and the real-cloud conformance tests
# (cloud-conformance.yml, job s3) share. make plan, apply and destroy pass
# these and deploy/ci/lab-vars.sh's inputs (the bootstrap's conformance role
# as the app role). The name keeps the bucket the environment has had since
# before modules (moved.tf); a local run writes under local/, and the
# conformance tests' conformance/ prefix expires after a day, in this
# environment only (decision 0030). The README's "How this scenario is
# tested" lists them.

TF_VARS = -var name=artifactweave-acc -var region=$(LAB_AWS_REGION) -var store_prefix=local/ \
  -var expiring_prefixes='{"conformance/" = 1}'

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
