output "account" {
  description = "Storage account for blob.azure.account and ARTIFACTWEAVE_TEST_AZURE_ACCOUNT."
  value       = azurerm_storage_account.blobs.name
}

output "container" {
  description = "Container for blob.azure.container and ARTIFACTWEAVE_TEST_AZURE_CONTAINER."
  value       = azurerm_storage_container.blobs.name
}

output "store_prefix" {
  description = "Blob name prefix the local run writes under (blob.azure.prefix)."
  value       = var.store_prefix
}

output "resource_group" {
  description = "Resource group holding the account."
  value       = local.resource_group_name
}

output "subscription_id" {
  description = "Subscription holding the account."
  value       = var.subscription_id
}

output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml for this account. It authenticates through the Azure SDK chain, so artifactweave acts as whoever runs it (an Entra ID login, a managed identity or a workload identity) and signs download URLs with a user delegation SAS."
  value = yamlencode({
    blob = {
      backend = "azure"
      azure = {
        account    = azurerm_storage_account.blobs.name
        container  = azurerm_storage_container.blobs.name
        prefix     = var.store_prefix
        credential = "default"
      }
    }
  })
}

output "storage_host" {
  description = "Host a downloadRedirect repository's signed URLs point at."
  value       = "${azurerm_storage_account.blobs.name}.blob.core.windows.net"
}

output "expires_at" {
  description = "The expires-at tag (epoch seconds) a scenario run set, read by reap; empty outside scenario runs."
  value       = lookup(var.scenario_labels, "expires-at", "")
}
