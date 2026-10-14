# An acceptance storage account for artifactweave's azure blob backend: the
# local container (compose.yaml) and the real-cloud conformance tests
# (cloud-conformance.yml) both use it, under separate prefixes. Nothing uses
# an account key: Shared Key is disabled on the account.

data "azurerm_client_config" "current" {}

resource "random_string" "suffix" {
  length  = 8
  upper   = false
  special = false
}

resource "azurerm_resource_group" "this" {
  name     = "${var.name}-rg"
  location = var.location
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
  resource_group_name      = azurerm_resource_group.this.name
  location                 = azurerm_resource_group.this.location
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

# The identity GitHub Actions runs of one repository act as, through a
# federated credential: cloud-conformance.yml stores no secret.
resource "azurerm_user_assigned_identity" "github" {
  name                = "${var.name}-github"
  resource_group_name = azurerm_resource_group.this.name
  location            = azurerm_resource_group.this.location
}

resource "azurerm_federated_identity_credential" "github" {
  name                      = "github-${var.github_environment}"
  user_assigned_identity_id = azurerm_user_assigned_identity.github.id
  audience                  = ["api://AzureADTokenExchange"]
  issuer                    = "https://token.actions.githubusercontent.com"
  # Matched exactly. With GitHub's immutable subject the owner and
  # repository carry their IDs, so the plain repo:<owner>/<repo> form never
  # matches; the prefix comes from the repository's OIDC settings.
  subject = "${var.github_subject_prefix}:environment:${var.github_environment}"
}

# Storage Blob Data Contributor covers reading, writing and deleting blobs
# and generating user delegation keys (for SAS and presigned URLs). The
# operator holds it too: the local tests act as you, and the local container
# runs on a user delegation SAS minted from your login.
resource "azurerm_role_assignment" "github" {
  scope                = azurerm_storage_account.blobs.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = azurerm_user_assigned_identity.github.principal_id
  principal_type       = "ServicePrincipal"
}

resource "azurerm_role_assignment" "operator" {
  scope                = azurerm_storage_account.blobs.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = data.azurerm_client_config.current.object_id
}
