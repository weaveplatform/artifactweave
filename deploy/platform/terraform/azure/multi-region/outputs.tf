output "accounts" {
  description = "The member storage accounts, by region."
  value       = { for l, m in module.member : l => m.account }
}

output "resource_group" {
  description = "Resource group holding the accounts."
  value       = local.resource_group_name
}

output "subscription_id" {
  description = "Subscription holding the accounts."
  value       = var.subscription_id
}

output "artifactweave_config" {
  description = "The blob section of artifactweave.yaml: a mirror of the two accounts, preferring the primary region. Each store authenticates through the Azure SDK chain and signs download URLs with a user delegation SAS."
  value = yamlencode({
    blob = {
      backend = "mirror"
      stores = {
        for l in local.locations : local.members[l].store => {
          backend = "azure"
          zone    = l
          azure = {
            account    = module.member[l].account
            container  = module.member[l].container
            prefix     = var.store_prefix
            credential = "default"
          }
        }
      }
      mirror = {
        members      = [for l in local.locations : local.members[l].store]
        zone         = var.primary_location
        write_quorum = var.write_quorum
      }
    }
  })
}

output "storage_host" {
  description = "Host a downloadRedirect repository's signed URLs point at when the primary member signs them, as it does once it holds the blob."
  value       = module.member[var.primary_location].host
}

output "mirror_members" {
  description = "The mirror's members in order (this instance's zone first): store name, zone, and what a signed URL for one of its blobs starts with after the scheme. test/platform checks which member a redirect lands on with it."
  value = [
    for l in local.locations : {
      store             = local.members[l].store
      zone              = l
      signed_url_prefix = module.member[l].signed_url_prefix
    }
  ]
}

output "expires_at" {
  description = "The expires-at tag (epoch seconds) a scenario run set, read by reap; empty outside scenario runs."
  value       = lookup(var.scenario_labels, "expires-at", "")
}
