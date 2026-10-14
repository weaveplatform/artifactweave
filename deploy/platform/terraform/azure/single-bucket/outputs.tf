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
  description = "Resource group holding the account and the GitHub identity."
  value       = azurerm_resource_group.this.name
}

output "subscription_id" {
  description = "Subscription for azure/login (CONFORMANCE_AZURE_SUBSCRIPTION_ID)."
  value       = var.subscription_id
}

output "tenant_id" {
  description = "Tenant for azure/login (CONFORMANCE_AZURE_TENANT_ID)."
  value       = data.azurerm_client_config.current.tenant_id
}

output "client_id" {
  description = "The GitHub identity's client ID, for azure/login (CONFORMANCE_AZURE_CLIENT_ID)."
  value       = azurerm_user_assigned_identity.github.client_id
}
