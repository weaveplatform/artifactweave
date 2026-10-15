output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml: a mirror of the accounts, preferring the first location. Each store authenticates through the Azure SDK's default chain, as the app identity, and signs download URLs with a user delegation SAS."
  value = yamlencode({
    blob = {
      backend = "mirror"
      stores = {
        for l in var.locations : local.members[l].store => {
          backend = "azure"
          zone    = l
          azure = merge(
            { account = module.member[l].account, container = module.member[l].container, credential = "default" },
            var.store_prefix == "" ? {} : { prefix = var.store_prefix },
          )
        }
      }
      mirror = {
        members      = [for l in var.locations : local.members[l].store]
        zone         = var.locations[0]
        write_quorum = var.write_quorum
      }
    }
  })
}

output "storage_host" {
  description = "Where a downloadRedirect repository's signed URLs point: the first member's blob endpoint, which signs once it holds the blob."
  value       = module.member[var.locations[0]].host
}

output "accounts" {
  description = "The member storage accounts, by location."
  value       = { for l, m in module.member : l => m.account }
}

output "resource_group" {
  description = "The resource group holding the accounts."
  value       = local.resource_group_name
}
