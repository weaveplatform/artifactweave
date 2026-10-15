# The long-lived foundation every Azure scenario stands on (decision 0030):
# GitHub's trust in two managed identities, the resource group scenarios run
# in, the remote state account, the DNS zone and a budget. An operator
# applies it once; scenarios come and go without touching it.

data "azurerm_client_config" "current" {}

locals {
  conformance_subject = "${var.github_subject_prefix}:environment:${var.conformance_environment}"
  platform_subject    = "${var.github_subject_prefix}:environment:${var.platform_environment}"

  # Built-in roles a scenario may grant (decision 0030): blob data access for
  # its own identities, for example Front Door's origin identity.
  scenario_grantable_roles = [
    "ba92f5b4-2d11-453d-a403-e96b0029c9fe", # Storage Blob Data Contributor
    "2a2b9908-6ea1-4ae2-8e65-a410df84e7d1", # Storage Blob Data Reader
  ]
}

resource "azurerm_resource_group" "bootstrap" {
  name     = var.resource_group
  location = var.location
  tags = {
    managed-by = "artifactweave-bootstrap"
    issue      = "126"
  }
}

# Every scenario instance is created here, named and labelled by scenario and
# instance, so the deployer's rights end at this boundary. An empty resource
# group costs nothing.
resource "azurerm_resource_group" "scenarios" {
  name     = var.scenarios_resource_group
  location = var.location
  tags = {
    managed-by = "artifactweave-bootstrap"
  }
}

# Front Door (the azure/cdn scenario) is the one namespace scenarios need
# that the subscription has not registered.
resource "azurerm_resource_provider_registration" "cdn" {
  name = "Microsoft.Cdn"
}

# The data-plane identity: blob rights on the conformance account only,
# granted by azure/single-bucket on its account.
resource "azurerm_user_assigned_identity" "conformance" {
  name                = "artifactweave-conformance"
  resource_group_name = azurerm_resource_group.bootstrap.name
  location            = azurerm_resource_group.bootstrap.location
}

# Matched exactly: Azure allows no wildcards in a federated credential, and
# GitHub's immutable subject carries the owner and repository IDs.
resource "azurerm_federated_identity_credential" "conformance" {
  name                      = "github-${var.conformance_environment}"
  user_assigned_identity_id = azurerm_user_assigned_identity.conformance.id
  audience                  = ["api://AzureADTokenExchange"]
  issuer                    = "https://token.actions.githubusercontent.com"
  subject                   = local.conformance_subject
}

# The deployer: creates and destroys scenario resources in the scenarios
# resource group, behind the cloud-platform environment's approval.
resource "azurerm_user_assigned_identity" "deployer" {
  name                = "artifactweave-deployer"
  resource_group_name = azurerm_resource_group.bootstrap.name
  location            = azurerm_resource_group.bootstrap.location
}

resource "azurerm_federated_identity_credential" "deployer" {
  name                      = "github-${var.platform_environment}"
  user_assigned_identity_id = azurerm_user_assigned_identity.deployer.id
  audience                  = ["api://AzureADTokenExchange"]
  issuer                    = "https://token.actions.githubusercontent.com"
  subject                   = local.platform_subject
}

# Creating, changing and deleting scenario resources, inside the scenarios
# resource group only.
resource "azurerm_role_assignment" "deployer_contributor" {
  scope                = azurerm_resource_group.scenarios.id
  role_definition_name = "Contributor"
  principal_id         = azurerm_user_assigned_identity.deployer.principal_id
  principal_type       = "ServicePrincipal"
}

# Scenario tests write and read blobs as the deployer; Contributor carries
# no data-plane rights.
resource "azurerm_role_assignment" "deployer_blob_data" {
  scope                = azurerm_resource_group.scenarios.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = azurerm_user_assigned_identity.deployer.principal_id
  principal_type       = "ServicePrincipal"
}

# Scenarios grant their own identities blob access (Front Door's origin
# identity, for one). The condition lets the deployer create and delete
# assignments of the blob data roles only, so it can never grant itself or
# anyone else Owner, Contributor or User Access Administrator.
resource "azurerm_role_assignment" "deployer_grants_blob_roles" {
  scope                = azurerm_resource_group.scenarios.id
  role_definition_name = "Role Based Access Control Administrator"
  principal_id         = azurerm_user_assigned_identity.deployer.principal_id
  principal_type       = "ServicePrincipal"
  condition_version    = "2.0"
  condition            = <<-EOT
    (
      (
        !(ActionMatches{'Microsoft.Authorization/roleAssignments/write'})
      )
      OR
      (
        @Request[Microsoft.Authorization/roleAssignments:RoleDefinitionId] ForAnyOfAnyValues:GuidEquals {${join(", ", local.scenario_grantable_roles)}}
      )
    )
    AND
    (
      (
        !(ActionMatches{'Microsoft.Authorization/roleAssignments/delete'})
      )
      OR
      (
        @Resource[Microsoft.Authorization/roleAssignments:RoleDefinitionId] ForAnyOfAnyValues:GuidEquals {${join(", ", local.scenario_grantable_roles)}}
      )
    )
  EOT
}

# Records in the scenarios' zone, for the registry and CDN hostnames and
# Front Door's domain validation.
resource "azurerm_role_assignment" "deployer_dns" {
  scope                = azurerm_dns_zone.azure.id
  role_definition_name = "DNS Zone Contributor"
  principal_id         = azurerm_user_assigned_identity.deployer.principal_id
  principal_type       = "ServicePrincipal"
}
