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

variable "user_dn_pattern" {
  type        = string
  default     = "uid={0},ou=people"
  description = "DN of a user relative to the URL's base DN, {0} standing for the login name."
}

variable "email_attribute" {
  type        = string
  default     = "mail"
  description = "Directory attribute holding a user's email address."
}

variable "auto_create_user" {
  type        = bool
  default     = true
  description = "Whether a directory user's first login creates a local user in the ldap realm."
}

variable "allow_user_to_access_profile" {
  type        = bool
  default     = false
  description = "Whether directory users may open their profile page."
}

variable "paging_support_enabled" {
  type        = bool
  default     = true
  description = "Whether searches ask the directory for paged results."
}

variable "search" {
  type = object({
    filter     = string
    base       = string
    sub_tree   = bool
    manager_dn = string
  })
  default     = null
  description = "Find users by a search filter under base, bound as manager_dn; null finds them by user_dn_pattern only and clears any stored manager password."
}

variable "manager_password" {
  type        = string
  sensitive   = true
  default     = null
  description = "Password of the search's manager DN, write-only on the server. Null keeps the stored password; Terraform retains this sensitive attribute in private state."
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
