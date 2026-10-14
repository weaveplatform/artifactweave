# azure/single-bucket: a storage account without Shared Key granted to the
# bootstrap's conformance identity (Terraform), artifactweave in a local
# container against the account, and the real-cloud conformance tests. Apply
# azure/bootstrap first. Plan and apply need az login and
# az account set --subscription SUBSCRIPTION.

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
# The identity, tenant and subscription variables, and the environment,
# come from azure/bootstrap.
GITHUB_VARS := \
  CONFORMANCE_AZURE_ACCOUNT=account \
  CONFORMANCE_AZURE_CONTAINER=container

.PHONY: check-subscription

## check-subscription: refuse unless az's active subscription and tenant are SUBSCRIPTION's
check-subscription:
	@../check-subscription.sh $(SUBSCRIPTION)

## LARGE_RANGES=1: with conformance, adds Put Block From URL ranges up to 4000MiB (about a minute)

include $(shell git rev-parse --show-toplevel)/deploy/platform/terraform/scenario.mk
