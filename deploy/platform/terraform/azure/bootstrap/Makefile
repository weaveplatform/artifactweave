# azure/bootstrap: GitHub's trust in the conformance and deployer identities,
# the scenarios resource group, the remote state account, the DNS zone and a
# budget. Plan and apply need az login and
# az account set --subscription SUBSCRIPTION.

# Every target that touches Azure checks that the CLI's active subscription
# is this one first; Terraform and the backend are pinned to it too.
SUBSCRIPTION ?= c33dbc35-f86d-4b7c-a81e-2a33eb3f7946
GUARD := check-subscription
TF_VARS := -var subscription_id=$(SUBSCRIPTION)
STATE_PROBE := az storage account show --subscription $(SUBSCRIPTION) \
  --resource-group artifactweave-bootstrap-rg --name awtfstatec33dbc35
GITHUB_VARS := \
  CONFORMANCE_AZURE_CLIENT_ID=conformance_client_id \
  CONFORMANCE_AZURE_TENANT_ID=tenant_id \
  CONFORMANCE_AZURE_SUBSCRIPTION_ID=subscription_id \
  PLATFORM_AZURE_CLIENT_ID=deployer_client_id \
  PLATFORM_AZURE_TENANT_ID=tenant_id \
  PLATFORM_AZURE_SUBSCRIPTION_ID=subscription_id \
  PLATFORM_AZURE_RESOURCE_GROUP=scenarios_resource_group \
  PLATFORM_AZURE_STATE_ACCOUNT=state_storage_account

.PHONY: check-subscription

## check-subscription: refuse unless az's active subscription and tenant are SUBSCRIPTION's
check-subscription:
	@../check-subscription.sh $(SUBSCRIPTION)

include $(shell git rev-parse --show-toplevel)/deploy/platform/terraform/bootstrap.mk
