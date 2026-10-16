# One storage account and private container, without Shared Key, with
# Storage Blob Data Contributor for the principals in var.principals.
# Scenarios call it once per store; it holds no provider block, so the
# caller's azurerm provider and subscription apply (decision 0030).

locals {
  # As the azure backend normalises blob.azure.prefix, so the management
  # policy matches the blobs it writes.
  trimmed      = trimprefix(var.store_prefix, "/")
  store_prefix = local.trimmed == "" || endswith(local.trimmed, "/") ? local.trimmed : "${local.trimmed}/"
}

# Accepted trivy findings (deploy/check.sh), each with its reason:
# - AZU-0012: by default the account takes connections from any network,
#   because artifactweave and the clients its signed URLs serve have no fixed
#   addresses. Shared Key is off, so every request still needs an Entra ID
#   token or a user delegation SAS. A caller that serves reads through Front
#   Door's Private Link (azure/cdn) narrows it with public_network_access and
#   network_rules.
# - AZU-0057: no Storage Analytics logs; the Azure activity log records the
#   management calls.
# - AZU-0058: replication_type defaults to LRS. A mirror member keeps one
#   region's copy, and the mirror the other; a caller that wants Azure's own
#   geo-replication asks for it (geo-replicated).
# - AZU-0060: Microsoft-managed keys; a customer-managed key would add a Key
#   Vault and its access policies to manage.
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
  account_replication_type = var.replication_type
  min_tls_version          = "TLS1_2"

  https_traffic_only_enabled      = true
  allow_nested_items_to_be_public = false
  public_network_access           = var.public_network_access
  # Entra ID only: no account keys, so none can leak. The backend reaches it
  # with Entra ID or a user delegation SAS.
  shared_access_key_enabled       = false
  default_to_oauth_authentication = true

  # Only the networks given reach the public endpoint. Private endpoints,
  # Front Door's among them, are not subject to these rules.
  dynamic "network_rules" {
    for_each = var.network_rules == null ? [] : [var.network_rules]
    content {
      default_action             = "Deny"
      bypass                     = ["AzureServices"]
      ip_rules                   = network_rules.value.ip_rules
      virtual_network_subnet_ids = network_rules.value.subnet_ids
    }
  }

  tags = var.tags
}

resource "azurerm_storage_container" "blobs" {
  name                  = var.container
  storage_account_id    = azurerm_storage_account.blobs.id
  container_access_type = "private"
}

# Backstops for runs that stop before cleaning up: abandoned temporary blobs,
# and the caller's expiring prefixes. Uncommitted blocks are not blobs; Azure
# discards them after seven days on its own. The rules keep this order; the
# tests address them by position.
resource "azurerm_storage_management_policy" "cleanup" {
  storage_account_id = azurerm_storage_account.blobs.id

  rule {
    name    = "abandoned-uploads"
    enabled = true
    filters {
      blob_types   = ["blockBlob"]
      prefix_match = ["${var.container}/${local.store_prefix}_uploads/"]
    }
    actions {
      base_blob {
        delete_after_days_since_modification_greater_than = 2
      }
    }
  }

  dynamic "rule" {
    for_each = var.expiring_prefixes
    content {
      # Rule names take letters, digits and hyphens only.
      name    = "expire-${trim(replace(rule.key, "/[^A-Za-z0-9]+/", "-"), "-")}"
      enabled = true
      filters {
        blob_types   = ["blockBlob"]
        prefix_match = ["${var.container}/${rule.key}"]
      }
      actions {
        base_blob {
          delete_after_days_since_modification_greater_than = rule.value
        }
      }
    }
  }
}

# Storage Blob Data Contributor covers reading, listing, writing and deleting
# blobs and generating user delegation keys (for SAS and presigned URLs),
# and nothing on the account itself. The keys of var.principals are static
# names, so for_each plans before the principal IDs are known.
resource "azurerm_role_assignment" "app" {
  for_each             = var.principals
  scope                = azurerm_storage_account.blobs.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = each.value.principal_id
  principal_type       = each.value.principal_type
}
