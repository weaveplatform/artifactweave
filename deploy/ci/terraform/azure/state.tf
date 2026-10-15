# Remote state for this bootstrap (bootstrap/azure.tfstate) and every
# scenario run (scenarios/<cloud>/<scenario>/<instance>), so an apply
# dispatch and a later destroy dispatch find the same state (decision 0030).
# It holds kilobytes; a Standard account has no fixed fee, so it stays when
# scenarios are torn down.
#
# Accepted trivy findings (deploy/check.sh), each with its reason:
# - AZU-0012: the account takes connections from any network, because the
#   runners and laptops have no fixed addresses. Shared Key is off, so every
#   request needs an Entra ID token with a data role on the account.
# - AZU-0057: no Storage Analytics logs; the activity log records the
#   management calls.
# - AZU-0058: LRS. Losing the region would lose kilobytes of lab state that
#   a fresh bootstrap and import can rebuild; versioning already covers a
#   bad apply, and the GCP state bucket is regional for the same reason.
# - AZU-0060: Microsoft-managed keys. A customer-managed key would add a Key
#   Vault with its own fees for kilobytes of state.
# - AZU-0061: no infrastructure (double) encryption; service-side encryption
#   applies, and the setting can only be chosen when the account is created.
#trivy:ignore:AZU-0012
#trivy:ignore:AZU-0057
#trivy:ignore:AZU-0058
#trivy:ignore:AZU-0060
#trivy:ignore:AZU-0061
resource "azurerm_storage_account" "state" {
  # Fixed: backend.tf names it, and a backend block cannot read a variable.
  name                     = "awtfstate${substr(replace(var.subscription_id, "-", ""), 0, 8)}"
  resource_group_name      = azurerm_resource_group.bootstrap.name
  location                 = azurerm_resource_group.bootstrap.location
  account_kind             = "StorageV2"
  account_tier             = "Standard"
  account_replication_type = "LRS"
  min_tls_version          = "TLS1_2"

  https_traffic_only_enabled      = true
  allow_nested_items_to_be_public = false
  shared_access_key_enabled       = false
  default_to_oauth_authentication = true

  # Every apply writes a new version; old ones are the undo history.
  blob_properties {
    versioning_enabled = true
    delete_retention_policy {
      days = 30
    }
    container_delete_retention_policy {
      days = 30
    }
  }
}

resource "azurerm_storage_container" "state" {
  name                  = "tfstate"
  storage_account_id    = azurerm_storage_account.state.id
  container_access_type = "private"
}

# The operator's login reads and writes the bootstrap's own state through
# Entra ID (backend.tf, use_azuread_auth); the deployer reads and writes the
# scenarios' state.
resource "azurerm_role_assignment" "operator_state" {
  scope                = azurerm_storage_account.state.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = data.azurerm_client_config.current.object_id
}

resource "azurerm_role_assignment" "deployer_state" {
  scope                = azurerm_storage_account.state.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = azurerm_user_assigned_identity.deployer.principal_id
  principal_type       = "ServicePrincipal"
}
