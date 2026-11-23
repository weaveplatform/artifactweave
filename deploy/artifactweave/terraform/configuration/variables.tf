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
