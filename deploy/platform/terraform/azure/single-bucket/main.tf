# An acceptance storage account for artifactweave's azure blob backend: the
# local container (compose.yaml) and the real-cloud conformance tests
# (cloud-conformance.yml) both use it, under separate prefixes. Nothing uses
# an account key: Shared Key is disabled on the account. The conformance
# identity and GitHub's trust in it live in azure/bootstrap, which outlives
# this scenario; apply the bootstrap first.

data "azurerm_client_config" "current" {}

resource "random_string" "suffix" {
  length  = 8
  upper   = false
  special = false
}

# Locally this root creates its own resource group. A scenario run applies as
# the deployer, whose rights end at azure/bootstrap's scenarios resource
# group, so it names that group instead and every resource lands inside it.
resource "azurerm_resource_group" "this" {
  count    = var.resource_group_name == "" ? 1 : 0
  name     = "${var.name}-rg"
  location = var.location
  tags     = var.scenario_labels
}

data "azurerm_resource_group" "existing" {
  count = var.resource_group_name == "" ? 0 : 1
  name  = var.resource_group_name
}

locals {
  resource_group_name = var.resource_group_name == "" ? azurerm_resource_group.this[0].name : data.azurerm_resource_group.existing[0].name
  location            = var.resource_group_name == "" ? azurerm_resource_group.this[0].location : data.azurerm_resource_group.existing[0].location
}

moved {
  from = azurerm_resource_group.this
  to   = azurerm_resource_group.this[0]
}

# Accepted trivy findings (deploy/check.sh), each with its reason:
# - AZU-0012: the account takes connections from any network, because the
#   conformance runners and laptops have no fixed addresses. Shared Key is
#   off, so every request still needs an Entra ID token or a user delegation
#   SAS; the azure/cdn scenario is the one that closes the public endpoint.
# - AZU-0057: no Storage Analytics logs; the Azure activity log records the
#   management calls, and these are acceptance data only.
# - AZU-0058: LRS on purpose. Acceptance data needs no second region, and the
#   geo-replicated scenario is where GZRS is shown.
# - AZU-0060: Microsoft-managed keys are enough for acceptance data; a
#   customer-managed key would add a Key Vault the tests never exercise.
# - AZU-0061: no infrastructure (double) encryption. Service-side encryption
#   already applies, and the setting can only be chosen when the account is
#   created.
#trivy:ignore:AZU-0012
#trivy:ignore:AZU-0057
#trivy:ignore:AZU-0058
#trivy:ignore:AZU-0060
#trivy:ignore:AZU-0061
resource "azurerm_storage_account" "blobs" {
  # 3 to 24 lowercase letters and digits, globally unique.
  name                     = "awacc${random_string.suffix.result}"
  resource_group_name      = local.resource_group_name
  location                 = local.location
  account_kind             = "StorageV2"
  account_tier             = "Standard"
  account_replication_type = "LRS"
  min_tls_version          = "TLS1_2"

  https_traffic_only_enabled      = true
  allow_nested_items_to_be_public = false
  # Entra ID only: no account keys, so none can leak. The backend reaches it
  # with Entra ID or a user delegation SAS.
  shared_access_key_enabled       = false
  default_to_oauth_authentication = true

  # Scenario runs (platform-scenarios.yml) tag the account with the
  # scenario, the instance and expires-at, which reap matches.
  tags = var.scenario_labels
}

resource "azurerm_storage_container" "blobs" {
  name                  = var.container
  storage_account_id    = azurerm_storage_account.blobs.id
  container_access_type = "private"
}

# Backstops for runs that stop before cleaning up: abandoned temporary
# blobs and conformance prefixes. Uncommitted blocks are not blobs; Azure
# discards them after seven days on its own.
resource "azurerm_storage_management_policy" "cleanup" {
  storage_account_id = azurerm_storage_account.blobs.id

  rule {
    name    = "abandoned-uploads"
    enabled = true
    filters {
      blob_types   = ["blockBlob"]
      prefix_match = ["${var.container}/${var.store_prefix}_uploads/"]
    }
    actions {
      base_blob {
        delete_after_days_since_modification_greater_than = 2
      }
    }
  }

  rule {
    name    = "conformance-runs"
    enabled = true
    filters {
      blob_types   = ["blockBlob"]
      prefix_match = ["${var.container}/conformance/"]
    }
    actions {
      base_blob {
        delete_after_days_since_modification_greater_than = 1
      }
    }
  }
}

# The data-plane identity azure/bootstrap created and GitHub's
# cloud-conformance environment acts as. A scenario run sets
# conformance_identity to empty: the deployer cannot read the bootstrap's
# resource group, and the job runs artifactweave as the deployer, which the
# operator assignment below covers.
data "azurerm_user_assigned_identity" "conformance" {
  count               = var.conformance_identity == "" ? 0 : 1
  name                = var.conformance_identity
  resource_group_name = var.bootstrap_resource_group
}

# Storage Blob Data Contributor covers reading, writing and deleting blobs
# and generating user delegation keys (for SAS and presigned URLs). The
# operator holds it too: the local tests act as you, and the local container
# runs on a user delegation SAS minted from your login.
resource "azurerm_role_assignment" "conformance" {
  count                = var.conformance_identity == "" ? 0 : 1
  scope                = azurerm_storage_account.blobs.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = data.azurerm_user_assigned_identity.conformance[0].principal_id
  principal_type       = "ServicePrincipal"
}

moved {
  from = azurerm_role_assignment.conformance
  to   = azurerm_role_assignment.conformance[0]
}

resource "azurerm_role_assignment" "operator" {
  scope                = azurerm_storage_account.blobs.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = data.azurerm_client_config.current.object_id
}
