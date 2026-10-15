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
