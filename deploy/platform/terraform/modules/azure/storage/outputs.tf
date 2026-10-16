output "id" {
  description = "The storage account's resource ID, which a Front Door origin's Private Link targets."
  value       = azurerm_storage_account.blobs.id
}

output "account" {
  description = "The storage account's name, for blob.azure.account."
  value       = azurerm_storage_account.blobs.name
}

output "container" {
  description = "The container's name, for blob.azure.container."
  value       = azurerm_storage_container.blobs.name
}

output "host" {
  description = "The account's blob endpoint host, where its signed URLs point."
  value       = "${azurerm_storage_account.blobs.name}.blob.core.windows.net"
}

output "signed_url_prefix" {
  description = "What a signed URL for a blob in this container starts with after the scheme."
  value       = "${azurerm_storage_account.blobs.name}.blob.core.windows.net/${azurerm_storage_container.blobs.name}/"
}

output "secondary_host" {
  description = "The blob endpoint host of Azure's read-only copy in the paired region (<account>-secondary), which a user delegation SAS minted for the account also reads, when replication_type is RAGRS or RAGZRS; null otherwise, since a GRS or GZRS copy can be read only after a failover."
  value       = contains(["RAGRS", "RAGZRS"], var.replication_type) ? "${azurerm_storage_account.blobs.name}-secondary.blob.core.windows.net" : null
}

output "network_access" {
  description = "Who reaches the account's public endpoint: public is Enabled or Disabled, and with Enabled, ip_rules and subnet_ids list the only networks it answers, or are empty when it answers any."
  value = {
    public     = var.public_network_access
    ip_rules   = try(var.network_rules.ip_rules, [])
    subnet_ids = try(var.network_rules.subnet_ids, [])
  }
}
