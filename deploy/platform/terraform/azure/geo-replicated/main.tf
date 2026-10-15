# One storage account that Azure replicates to the paired region
# (read-access geo-zone-redundant, RA-GZRS, by default), for operators who
# want the cloud's own replication rather than artifactweave's mirror.
# artifactweave writes to the primary; Azure copies every blob to the
# secondary, which serves reads at <account>-secondary.

resource "azurerm_resource_group" "this" {
  count    = var.resource_group_name == null ? 1 : 0
  name     = "${var.name}-rg"
  location = var.location
  tags     = var.tags
}

resource "random_string" "suffix" {
  length  = 6
  upper   = false
  special = false
}

locals {
  resource_group_name = var.resource_group_name != null ? var.resource_group_name : azurerm_resource_group.this[0].name
  # 24 characters at most: up to 15 of the name's, the suffix (6) and three
  # of the location's, as in azure/multi-region.
  account = "${substr(lower(replace(var.name, "/[^A-Za-z0-9]/", "")), 0, 15)}${random_string.suffix.result}${substr(var.location, 0, 3)}"
}

module "storage" {
  source = "../../modules/azure/storage"

  account_name        = local.account
  resource_group_name = local.resource_group_name
  location            = var.location
  replication_type    = var.replication_type
  container           = var.container
  store_prefix        = var.store_prefix
  principals          = merge({ app = { principal_id = var.app_principal_id } }, var.additional_principals)
  expiring_prefixes   = var.expiring_prefixes
  tags                = var.tags
}
