output "account" {
  description = "Storage account for blob.azure.account."
  value       = azurerm_storage_account.blobs.name
}

output "container" {
  description = "Container for blob.azure.container."
  value       = azurerm_storage_container.blobs.name
}

output "store_prefix" {
  description = "Blob name prefix artifactweave writes under (blob.azure.prefix)."
  value       = var.store_prefix
}

output "resource_group" {
  description = "Resource group holding the account."
  value       = local.resource_group_name
}

output "replication_type" {
  description = "The account's geo-redundant replication type."
  value       = azurerm_storage_account.blobs.account_replication_type
}

output "secondary_location" {
  description = "The paired region that holds Azure's copy."
  value       = azurerm_storage_account.blobs.secondary_location
}

output "secondary_storage_host" {
  description = "The secondary blob endpoint (<account>-secondary), readable with RAGZRS or RAGRS. A user delegation SAS minted for the primary reads it too."
  value       = azurerm_storage_account.blobs.secondary_blob_host
}

output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml for this account: one store, written at the primary. It authenticates through the Azure SDK chain and signs download URLs with a user delegation SAS."
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
  description = "Host a downloadRedirect repository's signed URLs point at (the primary)."
  value       = azurerm_storage_account.blobs.primary_blob_host
}

output "expires_at" {
  description = "The expires-at tag (epoch seconds) a scenario run set, read by reap; empty outside scenario runs."
  value       = lookup(var.scenario_labels, "expires-at", "")
}
