output "teams" {
  value       = artifactory_ldap_group_setting_v2.teams.name
  description = "Name of the STATIC or DYNAMIC group setting."
}

output "units" {
  value       = artifactory_ldap_group_setting_v2.units.name
  description = "Name of the HIERARCHICAL group setting."
}
