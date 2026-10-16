# A storage account behind Azure Front Door Premium, for operators who want
# artifactweave's downloads served from the edge (decision 0029).
# artifactweave writes to the account and answers a download from a
# downloadRedirect or cdnRedirect repository with a 302 to a SAS it signs,
# moved onto the Front Door host; Front Door passes the SAS through to
# storage over Private Link, so the account's public endpoint takes requests
# from artifactweave's networks only, or from none.

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
  # 24 characters at most, as in azure/single-bucket: up to 15 of the name's,
  # the suffix (6) and three of the location's.
  account = "${substr(lower(replace(var.name, "/[^A-Za-z0-9]/", "")), 0, 15)}${random_string.suffix.result}${substr(var.location, 0, 3)}"
  # With no networks of artifactweave's own, the public endpoint is off and
  # only private endpoints reach the account: Front Door's, and the one
  # artifactweave's network needs.
  app_networks = length(var.app_ip_ranges) + length(var.app_subnet_ids) > 0
}

module "storage" {
  source = "../../modules/azure/storage"

  account_name          = local.account
  resource_group_name   = local.resource_group_name
  location              = var.location
  container             = var.container
  store_prefix          = var.store_prefix
  principals            = merge({ app = { principal_id = var.app_principal_id } }, var.additional_principals)
  expiring_prefixes     = var.expiring_prefixes
  public_network_access = local.app_networks ? "Enabled" : "Disabled"
  network_rules         = local.app_networks ? { ip_rules = var.app_ip_ranges, subnet_ids = var.app_subnet_ids } : null
  tags                  = var.tags
}

module "cdn" {
  source = "../../modules/azure/cdn"

  name                  = var.name
  resource_group_name   = local.resource_group_name
  origin                = { id = module.storage.id, host = module.storage.host, location = var.location }
  container             = module.storage.container
  store_prefix          = var.store_prefix
  private_link_location = var.private_link_location
  max_cache_age_minutes = var.redirect_ttl_minutes
  domain_name           = var.domain_name
  dns_zone_id           = var.dns_zone_id
  tags                  = var.tags
}
