resource "artifactory_proxy" "this" {
  key               = var.proxy_key
  host              = var.proxy_host
  port              = var.proxy_port
  username          = var.proxy_username
  password          = var.proxy_password
  platform_default  = var.proxy_platform_default
  redirect_to_hosts = var.proxy_redirect_to_hosts
  services          = var.proxy_services
}

resource "artifactory_property_set" "this" {
  name    = var.property_set_name
  visible = var.property_set_visible
  dynamic "property" {
    for_each = var.properties
    content {
      name                     = property.key
      closed_predefined_values = property.value.closed_predefined_values
      multiple_choice          = property.value.multiple_choice
      dynamic "predefined_value" {
        for_each = property.value.predefined_values
        content {
          name          = predefined_value.key
          default_value = predefined_value.value
        }
      }
    }
  }
}

# A backup definition is administered configuration: artifactweave stores and
# exports it, and P36 owns running, retaining and restoring backups.
resource "artifactory_backup" "this" {
  key                      = var.backup_key
  enabled                  = var.backup_enabled
  cron_exp                 = var.backup_cron_exp
  retention_period_hours   = var.backup_retention_period_hours
  excluded_repositories    = var.backup_excluded_repositories
  create_archive           = var.backup_create_archive
  exclude_new_repositories = var.backup_exclude_new_repositories
  send_mail_on_error       = var.backup_send_mail_on_error
  verify_disk_space        = var.backup_verify_disk_space
  export_mission_control   = var.backup_export_mission_control
}

resource "artifactory_mail_server" "this" {
  enabled         = var.mail_enabled
  host            = var.mail_host
  port            = var.mail_port
  username        = var.mail_username
  password        = var.mail_password
  from            = var.mail_from
  subject_prefix  = var.mail_subject_prefix
  artifactory_url = var.mail_artifactory_url
  use_tls         = var.mail_use_tls
  use_ssl         = var.mail_use_ssl
}
