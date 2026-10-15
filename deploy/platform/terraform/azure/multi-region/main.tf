# Two storage accounts in two regions as members of one artifactweave mirror
# (decision 0028): every blob is written to both, an upload succeeds once
# write_quorum of them hold it, and reads prefer the member in this
# instance's zone. The reference for regional resilience under artifactweave's
# control. Each account is LRS and nothing replicates between them: GRS
# copies are invisible to the mirror, and object replication refuses writes
# to its destination (409), which would break the mirror's own copy. The
# conformance identity lives in azure/bootstrap; apply it first.

data "azurerm_client_config" "current" {}

resource "random_string" "suffix" {
  length  = 8
  upper   = false
  special = false
}

# Locally this root creates its own resource group. A scenario run applies as
# the deployer, whose rights end at azure/bootstrap's scenarios resource
# group, so it names that group instead and both accounts land inside it. A
# resource group's region does not bind the regions of what it holds.
resource "azurerm_resource_group" "this" {
  count    = var.resource_group_name == "" ? 1 : 0
  name     = "${var.name}-rg"
  location = var.primary_location
  tags     = var.scenario_labels
}

data "azurerm_resource_group" "existing" {
  count = var.resource_group_name == "" ? 0 : 1
  name  = var.resource_group_name
}

# The data-plane identity azure/bootstrap created. A scenario run sets
# conformance_identity to empty: the deployer cannot read the bootstrap's
# resource group, and the job runs artifactweave as the deployer, which the
# operator principal covers.
data "azurerm_user_assigned_identity" "conformance" {
  count               = var.conformance_identity == "" ? 0 : 1
  name                = var.conformance_identity
  resource_group_name = var.bootstrap_resource_group
}

locals {
  resource_group_name = var.resource_group_name == "" ? azurerm_resource_group.this[0].name : data.azurerm_resource_group.existing[0].name
  # The mirror's members in order: this instance's zone first.
  locations = [var.primary_location, var.secondary_location]
  members = {
    for l in local.locations : l => {
      store = "azure_${l}"
      # 24 characters at most: awmr (4), the suffix (8) and up to ten of the
      # region's letters.
      account = "awmr${random_string.suffix.result}${substr(l, 0, min(length(l), 10))}"
    }
  }
  # Whoever applies (an operator, or the deployer in a scenario run, which
  # then runs artifactweave as itself) and the bootstrap's conformance
  # identity when it is named.
  principals = merge(
    { operator = data.azurerm_client_config.current.object_id },
    var.conformance_identity == "" ? {} : { conformance = data.azurerm_user_assigned_identity.conformance[0].principal_id },
  )
}

module "member" {
  source   = "../../modules/azure/storage"
  for_each = local.members

  account_name        = each.value.account
  resource_group_name = local.resource_group_name
  location            = each.key
  container           = var.container
  store_prefix        = var.store_prefix
  principals          = local.principals
  tags                = var.scenario_labels
}
