output "enable_anonymous_access" {
  value       = artifactory_general_security.this.enable_anonymous_access
  description = "Whether anonymous access is on, as read back from the server."
}
output "encryption_policy" {
  value       = artifactory_general_security.this.encryption_policy
  description = "The password encryption policy, as read back from the server."
}
