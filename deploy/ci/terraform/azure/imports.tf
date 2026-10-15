# Resources that exist before this root: adopted, not recreated. The
# resource group and DNS zone were created by CLI so the zone's name servers
# could be delegated at GoDaddy before this root existed.

import {
  to = azurerm_resource_group.bootstrap
  id = "/subscriptions/${var.subscription_id}/resourceGroups/${var.resource_group}"
}

import {
  to = azurerm_dns_zone.azure
  id = "/subscriptions/${var.subscription_id}/resourceGroups/${var.resource_group}/providers/Microsoft.Network/dnsZones/azure.artifact.weaveplatform.run"
}
