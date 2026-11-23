output "enabled" {
  value       = artifactory_trashcan_config.this.enabled
  description = "Whether the trash can is on."
}
output "retention_period_days" {
  value       = artifactory_trashcan_config.this.retention_period_days
  description = "Days a trashed item is kept before collection purges it."
}
