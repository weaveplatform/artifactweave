output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml for the account: one store, written at the primary. It authenticates through the Azure SDK's default chain, as the app identity, and signs download URLs with a user delegation SAS."
  value = yamlencode({
    blob = {
      backend = "azure"
      azure = merge(
        { account = module.storage.account, container = module.storage.container, credential = "default" },
        var.store_prefix == "" ? {} : { prefix = var.store_prefix },
      )
    }
  })
}

output "storage_host" {
  description = "Where a downloadRedirect repository's signed URLs point: the account's primary blob endpoint."
  value       = module.storage.host
}

output "account" {
  description = "The storage account (blob.azure.account)."
  value       = module.storage.account
}

output "container" {
  description = "The container in the account (blob.azure.container)."
  value       = module.storage.container
}

output "resource_group" {
  description = "The resource group holding the account."
  value       = local.resource_group_name
}

output "secondary_storage_host" {
  description = "The paired region's read endpoint (<account>-secondary), which a user delegation SAS minted for the primary also reads; null with GZRS or GRS, whose copy is readable only after a failover."
  value       = module.storage.secondary_host
}
