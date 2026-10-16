output "domain" {
  description = "Where artifactweave's Front Door URLs point (blob.azure.cdn.front_door.endpoint): domain_name, or the endpoint's own *.azurefd.net host."
  value       = local.custom ? var.domain_name : azurerm_cdn_frontdoor_endpoint.cdn.host_name
}

output "endpoint_host" {
  description = "The endpoint's own *.azurefd.net host, which domain_name's CNAME points at."
  value       = azurerm_cdn_frontdoor_endpoint.cdn.host_name
}

output "profile_name" {
  description = "The Front Door profile's name, for the portal and CLI."
  value       = azurerm_cdn_frontdoor_profile.cdn.name
}

output "profile_id" {
  description = "The Front Door profile's resource ID."
  value       = azurerm_cdn_frontdoor_profile.cdn.id
}

output "private_link_request_message" {
  description = "The request Front Door's private endpoint connection carries on the storage account, by which the connection to approve is found."
  value       = local.request_message
}

output "blob_path" {
  description = "The path the endpoint serves blobs under, /<container>/<store_prefix>sha256/; any other path answers 404 at the edge."
  value       = "/${var.container}/${local.store_prefix}sha256/"
}

output "cache_duration" {
  description = "How long an edge keeps a copy, as Front Door's rule writes it (HH:MM:SS)."
  value       = local.cache_duration
}
