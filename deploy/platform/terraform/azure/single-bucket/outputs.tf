output "account" {
  description = "Storage account for blob.azure.account and ARTIFACTWEAVE_TEST_AZURE_ACCOUNT."
  value       = azurerm_storage_account.blobs.name
}

output "container" {
  description = "Container for blob.azure.container and ARTIFACTWEAVE_TEST_AZURE_CONTAINER."
  value       = azurerm_storage_container.blobs.name
}

output "store_prefix" {
  description = "Blob name prefix the local container writes under (blob.azure.prefix)."
  value       = var.store_prefix
}

output "resource_group" {
  description = "Resource group holding the account."
  value       = azurerm_resource_group.this.name
}

output "subscription_id" {
  description = "Subscription holding the account, which credentials.sh checks the CLI against."
  value       = var.subscription_id
}
