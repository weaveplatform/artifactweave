# azure/single-bucket: a storage account without Shared Key granted to the
# bootstrap's conformance identity (Terraform), artifactweave run natively
# against it (make up, deploy/ci/local.sh), and the real-cloud conformance
# tests. Apply deploy/ci/terraform/azure first. Plan and apply need az login
# and az account set --subscription to the lab's (LAB_AZURE_SUBSCRIPTION in
# deploy/ci/lab.mk).

CONFORMANCE_ENV = \
  ARTIFACTWEAVE_TEST_AZURE_ACCOUNT=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw account) \
  ARTIFACTWEAVE_TEST_AZURE_CONTAINER=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw container) \
  ARTIFACTWEAVE_TEST_AZURE_LARGE_RANGES=$(LARGE_RANGES)
CONFORMANCE_RUN := TestContractOnRealAzure|TestRealAzureProbes
CONFORMANCE_PKG := ./pkg/blob/azure/
# The identity, tenant and subscription variables, and the environment,
# come from deploy/ci/terraform/azure.
GITHUB_VARS := \
  CONFORMANCE_AZURE_ACCOUNT=account \
  CONFORMANCE_AZURE_CONTAINER=container

## LARGE_RANGES=1: with conformance, adds Put Block From URL ranges up to 4000MiB (about a minute)

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
