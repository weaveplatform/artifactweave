output "artifactweave_config" {
  description = "The blob and download sections of artifactweave.yaml: one azure store, the account, with Front Door in front of it, and the URL lifetime the edge's cache cap matches. It authenticates through the Azure SDK's default chain, as the app identity, and signs user delegation SAS."
  value = yamlencode({
    blob = {
      backend = "azure"
      azure = merge(
        { account = module.storage.account, container = module.storage.container, credential = "default" },
        var.store_prefix == "" ? {} : { prefix = var.store_prefix },
        { cdn = { front_door = { endpoint = module.cdn.domain } } },
      )
    }
    download = { redirect = { ttl = "${var.redirect_ttl_minutes}m" } }
  })
}

output "storage_host" {
  description = "Where a downloadRedirect or cdnRedirect repository's signed URLs point: the CDN host, domain_name or the endpoint's *.azurefd.net host."
  value       = module.cdn.domain
}

output "origin_host" {
  description = "The account's blob endpoint, where a downloadRedirect repository's URLs point when a client sends X-JFrog-Download-Redirect-To: S3; it answers only artifactweave's networks."
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
  description = "The resource group holding the account and the Front Door profile."
  value       = local.resource_group_name
}

output "storage_account_id" {
  description = "The storage account's resource ID, where Front Door's private endpoint connection waits for approval."
  value       = module.storage.id
}

output "front_door_profile" {
  description = "The Front Door profile's name."
  value       = module.cdn.profile_name
}

output "front_door_endpoint_host" {
  description = "The Front Door endpoint's own *.azurefd.net host, which domain_name's CNAME points at."
  value       = module.cdn.endpoint_host
}

output "private_link_request_message" {
  description = "The request on Front Door's private endpoint connection to the account, which the approval looks for."
  value       = module.cdn.private_link_request_message
}
