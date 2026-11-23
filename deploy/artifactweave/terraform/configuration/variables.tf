variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory."
}
variable "access_token" {
  type        = string
  sensitive   = true
  description = "Admin access token, sent as a Bearer token."
}
variable "proxy_key" {
  type        = string
  default     = "outbound-proxy"
  description = "Unique key of the outbound proxy."
}
variable "proxy_host" {
  type        = string
  description = "Hostname or IP address of the outbound proxy."
}
variable "proxy_port" {
  type        = number
  default     = 3128
  description = "Port of the outbound proxy."
}
variable "proxy_username" {
  type        = string
  default     = null
  description = "Username for authenticated proxy connections."
}
variable "proxy_password" {
  type        = string
  sensitive   = true
  default     = null
  description = "Password for authenticated proxy connections; null clears it. Terraform retains this sensitive attribute in private state."
}
variable "proxy_platform_default" {
  type        = bool
  default     = false
  description = "Whether this proxy is the platform default. Keep proxy_services empty when true."
}
variable "proxy_redirect_to_hosts" {
  type        = set(string)
  default     = []
  description = "Hosts to which the proxy may redirect requests while retaining its credentials."
}
variable "proxy_services" {
  type        = set(string)
  default     = []
  description = "Services using this default proxy: jfrt, jfmc, jfxr or jfds."
}
variable "property_set_name" {
  type        = string
  default     = "release-properties"
  description = "Name of the property set."
}
variable "property_set_visible" {
  type        = bool
  default     = true
  description = "Whether the property set is visible and assignable."
}
variable "properties" {
  type = map(object({
    closed_predefined_values = optional(bool, false)
    multiple_choice          = optional(bool, false)
    predefined_values        = optional(map(bool), {})
  }))
  default = {
    release = {
      closed_predefined_values = true
      predefined_values        = { ready = true, waiting = false }
    }
    free = {}
  }
  description = "Property names, selection flags and predefined values mapped to whether each is selected by default."
}
variable "backup_key" {
  type        = string
  default     = "nightly"
  description = "Unique key of the backup definition: an XML name."
}
variable "backup_enabled" {
  type        = bool
  default     = true
  description = "Whether the backup definition is enabled."
}
variable "backup_cron_exp" {
  type        = string
  default     = "0 0 2 ? * MON-FRI"
  description = "Quartz cron expression of the backup schedule."
}
variable "backup_retention_period_hours" {
  type        = number
  default     = 168
  description = "Hours to keep a backup; 0 keeps every backup."
}
variable "backup_excluded_repositories" {
  type        = list(string)
  default     = null
  description = "Existing repositories to leave out of the backup; null excludes none. Keep it non-empty once set: the pinned provider omits an empty list, which leaves the stored one in place."
}
variable "backup_create_archive" {
  type        = bool
  default     = false
  description = "Whether a backup is written as a Zip archive."
}
variable "backup_exclude_new_repositories" {
  type        = bool
  default     = false
  description = "Whether repositories created later are left out of the backup."
}
variable "backup_send_mail_on_error" {
  type        = bool
  default     = true
  description = "Whether administrators are mailed when a backup fails."
}
variable "backup_verify_disk_space" {
  type        = bool
  default     = false
  description = "Whether a backup first checks the target has room (precalculate)."
}
variable "backup_export_mission_control" {
  type        = bool
  default     = false
  description = "The exportMissionControl flag of the backup definition."
}
variable "mail_enabled" {
  type        = bool
  default     = true
  description = "Whether mail notifications are sent."
}
variable "mail_host" {
  type        = string
  description = "SMTP server host name or IP address. Changing it replaces the mail server settings."
}
variable "mail_port" {
  type        = number
  default     = 25
  description = "SMTP server port."
}
variable "mail_username" {
  type        = string
  default     = null
  description = "SMTP AUTH username; null sends no credentials."
}
variable "mail_password" {
  type        = string
  sensitive   = true
  default     = null
  description = "SMTP AUTH password; null clears it. Sealed by the server and never returned; Terraform retains this sensitive attribute in private state."
}
variable "mail_from" {
  type        = string
  default     = null
  description = "Sender address of outgoing mail."
}
variable "mail_subject_prefix" {
  type        = string
  default     = "[artifactweave]"
  description = "Prefix of every outgoing subject. Set a value: a null prefix reads back as the [JFrog] default and plans a change."
}
variable "mail_artifactory_url" {
  type        = string
  description = "Registry URL linked from outgoing mail. Required here: the pinned provider reads an unset URL back as an empty string and plans a change."
}
variable "mail_use_tls" {
  type        = bool
  default     = false
  description = "Whether to upgrade the connection with STARTTLS."
}
variable "mail_use_ssl" {
  type        = bool
  default     = false
  description = "Whether to use implicit TLS from the first byte."
}
variable "layout_name" {
  type        = string
  default     = "release-layout"
  description = "Name of the repository layout: an XML name, unique among descriptor keys. Changing it replaces the layout."
}
variable "layout_artifact_path_pattern" {
  type        = string
  default     = "[org]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]"
  description = "Path pattern of the layout's artifacts, in layout tokens such as [org], [module], [baseRev] and [ext]."
}
variable "layout_descriptor_path_pattern" {
  type        = string
  default     = null
  description = "Path pattern of the layout's descriptors (such as a POM); null uses the artifact path pattern."
}
variable "layout_folder_integration_revision_regexp" {
  type        = string
  default     = "SNAPSHOT"
  description = "Regular expression of the integration revision in a folder name, without capturing groups; .* when not applicable."
}
variable "layout_file_integration_revision_regexp" {
  type        = string
  default     = "SNAPSHOT|(?:(?:[0-9]{8}.[0-9]{6})-(?:[0-9]+))"
  description = "Regular expression of the integration revision in a file name, without capturing groups; .* when not applicable."
}
