# azure/single-bucket: a storage account without Shared Key, a managed
# identity federated with GitHub Actions (Terraform), artifactweave in a
# local container against the account, and the real-cloud conformance tests.
# Plan and apply need az login and az account set --subscription SUBSCRIPTION.

# Every target that touches Azure checks that the CLI's active subscription
# is this one first; Terraform is pinned to it too.
SUBSCRIPTION ?= c33dbc35-f86d-4b7c-a81e-2a33eb3f7946
GUARD := check-subscription
TF_VARS := -var subscription_id=$(SUBSCRIPTION)
CONFORMANCE_DEPS := $(GUARD)
CONFORMANCE_ENV = \
  ARTIFACTWEAVE_TEST_AZURE_ACCOUNT=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw account) \
  ARTIFACTWEAVE_TEST_AZURE_CONTAINER=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw container) \
  ARTIFACTWEAVE_TEST_AZURE_LARGE_RANGES=$(LARGE_RANGES)
CONFORMANCE_RUN := TestContractOnRealAzure|TestRealAzureProbes
CONFORMANCE_PKG := ./pkg/blob/azure/
GITHUB_ENVIRONMENT := cloud-conformance
GITHUB_VARS := \
  CONFORMANCE_AZURE_ACCOUNT=account \
  CONFORMANCE_AZURE_CONTAINER=container \
  CONFORMANCE_AZURE_CLIENT_ID=client_id \
  CONFORMANCE_AZURE_TENANT_ID=tenant_id \
  CONFORMANCE_AZURE_SUBSCRIPTION_ID=subscription_id

.PHONY: check-subscription

## check-subscription: refuse unless az's active subscription and tenant are SUBSCRIPTION's
check-subscription:
	@./check-subscription.sh $(SUBSCRIPTION)

## LARGE_RANGES=1: with conformance, adds Put Block From URL ranges up to 4000MiB (about a minute)

include $(shell git rev-parse --show-toplevel)/deploy/platform/terraform/scenario.mk
