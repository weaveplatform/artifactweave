output "proxy_key" {
  value       = artifactory_proxy.this.key
  description = "Key of the configured outbound proxy."
}
output "property_set_name" {
  value       = artifactory_property_set.this.name
  description = "Name of the configured property set."
}
output "backup_key" {
  value       = artifactory_backup.this.key
  description = "Key of the configured backup definition."
}
output "mail_host" {
  value       = artifactory_mail_server.this.host
  description = "SMTP host of the configured mail server."
}
