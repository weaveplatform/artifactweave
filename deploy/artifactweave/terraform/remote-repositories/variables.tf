variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory."
}

variable "access_token" {
  type        = string
  sensitive   = true
  description = "Admin access token, sent as a Bearer token."
}

variable "repository_prefix" {
  type        = string
  default     = "managed-remote"
  description = "Prefix for all fourteen remote repository keys."
}

variable "upstream_urls" {
  type        = map(string)
  description = "Upstream base URL for each of the fourteen package formats in this reference. Terraform registry and provider endpoints are derived separately from its base URL."
  validation {
    condition     = alltrue([for name in ["generic", "docker", "oci", "helmoci", "npm", "go", "maven", "gradle", "ivy", "sbt", "pypi", "nuget", "helm", "terraform"] : can(regex("^https?://", var.upstream_urls[name]))])
    error_message = "Supply an HTTP(S) upstream URL for every supported format."
  }
}

variable "revision" {
  type        = number
  default     = 1
  description = "Configuration revision, 1 or 2. Revision 2 demonstrates updates, disables generic persistence and takes that repository offline."
  validation {
    condition     = contains([1, 2], var.revision)
    error_message = "Use revision 1 or 2."
  }
}

variable "upstream_password" {
  type        = string
  sensitive   = true
  ephemeral   = true
  description = "Generic upstream password passed to the provider's write-only attribute; never stored in Terraform state. Rotate it by increasing revision."
}

variable "header_secret" {
  type        = string
  sensitive   = true
  description = "Sensitive generic upstream header value. Terraform stores it in state; artifactweave seals it and redacts API reads."
}

variable "proxy_key" {
  type        = string
  description = "Existing proxy definition referenced by the generic remote. Manage it through the YAML descriptor until P12 implements XML descriptor reads used by artifactory_proxy."
}
