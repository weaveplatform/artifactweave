variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory."
}

variable "access_token" {
  type        = string
  sensitive   = true
  description = "Admin access token, sent as a Bearer token."
}

variable "connection_key" {
  type        = string
  default     = "corp"
  description = "Key of the LDAP connection the group settings read through."
}

variable "ldap_url" {
  type        = string
  default     = "ldap://ldap.example.com:389/dc=example,dc=org"
  description = "URL of the directory, with its base DN."
}

variable "name_prefix" {
  type        = string
  default     = "corp"
  description = "Prefix of the two group settings' names."
}

variable "strategy" {
  type        = string
  default     = "STATIC"
  description = "How the teams setting maps memberships: STATIC (groups list their members) or DYNAMIC (users list their groups in memberOf)."
  validation {
    condition     = contains(["STATIC", "DYNAMIC"], var.strategy)
    error_message = "strategy must be STATIC or DYNAMIC."
  }
}

variable "sub_tree" {
  type        = bool
  default     = false
  description = "Whether the teams setting searches below ou=groups; the units setting takes the opposite."
}
