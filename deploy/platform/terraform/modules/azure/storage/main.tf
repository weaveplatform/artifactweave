# One storage account and private container a scenario stores blobs in,
# without Shared Key, with Storage Blob Data Contributor for the principals
# artifactweave runs as. Scenarios call it once per store (multi-region calls
# it per region); it holds no provider block, so the caller's azurerm
# provider and subscription apply (decision 0030).

# Accepted trivy findings (deploy/check.sh), each with its reason:
# - AZU-0012: the account takes connections from any network, because CI
#   runners and laptops have no fixed addresses. Shared Key is off, so every
#   request still needs an Entra ID token or a user delegation SAS; the
#   azure/cdn scenario is the one that closes the public endpoint.
# - AZU-0057: no Storage Analytics logs; the Azure activity log records the
#   management calls, and these are scenario data only.
# - AZU-0058: LRS on purpose. A scenario that needs a second region gets it
#   from artifactweave's mirror or shows the cloud's own (geo-replicated).
# - AZU-0060: Microsoft-managed keys are enough for scenario data; a
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
  name                     = var.account_name
  resource_group_name      = var.resource_group_name
  location                 = var.location
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

  tags = var.tags
}

resource "azurerm_storage_container" "blobs" {
  name                  = var.container
  storage_account_id    = azurerm_storage_account.blobs.id
  container_access_type = "private"
}

# A backstop for runs that stop before cleaning up: abandoned temporary
# blobs. Uncommitted blocks are not blobs; Azure discards them after seven
# days on its own.
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
}

# Storage Blob Data Contributor covers reading, writing and deleting blobs
# and generating user delegation keys (for SAS and presigned URLs). The keys
# of var.principals are static names, so for_each plans before the principal
# IDs are known.
resource "azurerm_role_assignment" "app" {
  for_each             = var.principals
  scope                = azurerm_storage_account.blobs.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = each.value
}
