# Storage accounts in two or more regions as members of one artifactweave
# mirror (decision 0028): every blob is written to each, an upload succeeds
# once write_quorum of them hold it, and reads prefer the member in this
# instance's zone. Each account is LRS and nothing replicates between them:
# GRS copies are invisible to the mirror, and object replication refuses
# writes to its destination (409), which would break the mirror's own copy.

resource "azurerm_resource_group" "this" {
  count    = var.resource_group_name == null ? 1 : 0
  name     = "${var.name}-rg"
  location = var.locations[0]
  tags     = var.tags
}

# One suffix per account: the location's first letters, which follow it,
# need not tell two regions apart (southafricanorth, southafricawest), and
# a suffix keyed by location keeps each account's name when the order of
# locations changes.
resource "random_string" "suffix" {
  for_each = toset(var.locations)
  length   = 6
  upper    = false
  special  = false
}

locals {
  resource_group_name = var.resource_group_name != null ? var.resource_group_name : azurerm_resource_group.this[0].name
  # 24 characters at most: up to 15 of the name's, the suffix (6) and three
  # of the location's.
  stem = substr(lower(replace(var.name, "/[^A-Za-z0-9]/", "")), 0, 15)
  members = {
    for l in var.locations : l => {
      store   = "azure_${l}"
      account = "${local.stem}${random_string.suffix[l].result}${substr(l, 0, 3)}"
    }
  }
}

module "member" {
  source   = "../../modules/azure/storage"
  for_each = local.members

  account_name        = each.value.account
  resource_group_name = local.resource_group_name
  location            = each.key
  container           = var.container
  store_prefix        = var.store_prefix
  principals          = merge({ app = { principal_id = var.app_principal_id } }, var.additional_principals)
  expiring_prefixes   = var.expiring_prefixes
  tags                = var.tags
}
