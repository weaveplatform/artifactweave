# The connection the group settings read through. It stays disabled: the
# settings are qualified as stored and served, and a disabled connection
# neither turns internal password login off nor asks a directory at login.
resource "artifactory_ldap_setting_v2" "directory" {
  key             = var.connection_key
  enabled         = false
  ldap_url        = var.ldap_url
  user_dn_pattern = "uid={0},ou=people"
}

# The Access API's group setting, which the provider refreshes after each
# write.
resource "artifactory_ldap_group_setting_v2" "teams" {
  name                   = "${var.name_prefix}-teams"
  enabled_ldap           = artifactory_ldap_setting_v2.directory.key
  group_base_dn          = "ou=groups"
  group_name_attribute   = "cn"
  group_member_attribute = var.strategy == "STATIC" ? "uniqueMember" : "memberOf"
  filter                 = "(objectClass=groupOfUniqueNames)"
  description_attribute  = "description"
  strategy               = var.strategy
  sub_tree               = var.sub_tree
}

# The descriptor's security.ldapGroupSettings entry, written by YAML PATCH
# and read back from the XML descriptor.
resource "artifactory_ldap_group_setting" "units" {
  name                   = "${var.name_prefix}-units"
  ldap_setting_key       = artifactory_ldap_setting_v2.directory.key
  group_base_dn          = ""
  group_name_attribute   = "ou"
  group_member_attribute = "ou"
  filter                 = "(objectClass=organizationalUnit)"
  description_attribute  = "description"
  strategy               = "HIERARCHICAL"
  sub_tree               = !var.sub_tree

  # The provider deletes this resource by reading every group setting,
  # clearing security.ldapGroupSettings and writing the others back. A
  # delete of teams that lands in between would be undone by that write, so
  # Terraform must never run the two deletes at once: this one goes first.
  depends_on = [artifactory_ldap_group_setting_v2.teams]
}
