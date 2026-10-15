# The Azure bootstrap (deploy/ci/terraform/azure): GitHub's trust in the
# conformance and deployer identities, the scenarios resource group, the
# remote state account, the DNS zone and a budget. Plan and apply need az login
# and az account set --subscription <LAB_AZURE_SUBSCRIPTION in deploy/ci/lab.mk>;
# every target that touches Azure checks that subscription first, and
# Terraform and the backend are pinned to it too. Run from the repository root
# with make ci-<target> CLOUD=azure.

TF_VARS = -var subscription_id=$(LAB_AZURE_SUBSCRIPTION)
STATE_PROBE = az storage account show --subscription $(LAB_AZURE_SUBSCRIPTION) \
  --resource-group $(LAB_AZURE_BOOTSTRAP_RG) --name awtfstatec33dbc35
GITHUB_VARS := \
  CONFORMANCE_AZURE_CLIENT_ID=conformance_client_id \
  CONFORMANCE_AZURE_TENANT_ID=tenant_id \
  CONFORMANCE_AZURE_SUBSCRIPTION_ID=subscription_id \
  PLATFORM_AZURE_CLIENT_ID=deployer_client_id \
  PLATFORM_AZURE_TENANT_ID=tenant_id \
  PLATFORM_AZURE_SUBSCRIPTION_ID=subscription_id \
  PLATFORM_AZURE_RESOURCE_GROUP=scenarios_resource_group \
  PLATFORM_AZURE_STATE_ACCOUNT=state_storage_account

include $(shell git rev-parse --show-toplevel)/deploy/ci/bootstrap.mk
