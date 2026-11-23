# The connection the group settings read through. It stays disabled: the
# settings are qualified as stored and served, and a disabled connection
# neither turns internal password login off nor asks a directory at login.
#
# The provider requires the four search fields and manager_password together
# and leaves an empty password out of its request, which keeps the stored
# one; so a null manager_password with a search keeps it, and dropping the
# search clears the manager DN and with it the stored password.
resource "artifactory_ldap_setting_v2" "directory" {
  key                          = var.connection_key
  enabled                      = false
  ldap_url                     = var.ldap_url
  user_dn_pattern              = var.user_dn_pattern
  email_attribute              = var.email_attribute
  auto_create_user             = var.auto_create_user
  allow_user_to_access_profile = var.allow_user_to_access_profile
  paging_support_enabled       = var.paging_support_enabled
  search_filter                = var.search == null ? null : var.search.filter
  search_base                  = var.search == null ? null : var.search.base
  search_sub_tree              = var.search == null ? null : var.search.sub_tree
  manager_dn                   = var.search == null ? null : var.search.manager_dn
  manager_password             = var.search == null ? null : (var.manager_password == null ? "" : var.manager_password)
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

# A second Access API group setting, mapping the `ou` RDNs of a user's DN.
# The provider refuses sub_tree with HIERARCHICAL, so it stays off. The
# deprecated artifactory_ldap_group_setting is not used: it reads
# security.ldapGroupSettings back from the XML descriptor, which 7.x's
# descriptor and artifactweave's no longer carry (JFrog's own tests of it
# skip from Artifactory 7.57.1), so it cannot read a setting back.
resource "artifactory_ldap_group_setting_v2" "units" {
  name                   = "${var.name_prefix}-units"
  enabled_ldap           = artifactory_ldap_setting_v2.directory.key
  group_name_attribute   = "ou"
  group_member_attribute = "ou"
  filter                 = "(objectClass=organizationalUnit)"
  description_attribute  = "description"
  strategy               = "HIERARCHICAL"
  sub_tree               = false
}
