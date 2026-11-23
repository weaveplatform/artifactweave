output "proxy_key" {
  value       = artifactory_proxy.this.key
  description = "Key of the configured outbound proxy."
}
output "property_set_name" {
  value       = artifactory_property_set.this.name
  description = "Name of the configured property set."
}
