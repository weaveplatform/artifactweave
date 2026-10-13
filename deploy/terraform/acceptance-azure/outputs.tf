output "account" {
  description = "Storage account for blob.azure.account and ARTIFACTWEAVE_TEST_AZURE_ACCOUNT."
  value       = azurerm_storage_account.blobs.name
}

output "container" {
  value = azurerm_storage_container.blobs.name
}

output "store_prefix" {
  value = var.store_prefix
}

output "resource_group" {
  value = azurerm_resource_group.this.name
}

output "subscription_id" {
  value = var.subscription_id
}

output "tenant_id" {
  value = data.azurerm_client_config.current.tenant_id
}

output "client_id" {
  description = "The GitHub identity's client ID, for azure/login (CONFORMANCE_AZURE_CLIENT_ID)."
  value       = azurerm_user_assigned_identity.github.client_id
}
