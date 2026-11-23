output "teams" {
  value       = artifactory_ldap_group_setting_v2.teams.name
  description = "Name of the Access API group setting."
}

output "units" {
  value       = artifactory_ldap_group_setting.units.name
  description = "Name of the descriptor group setting."
}
