variable "url" {
  type        = string
  description = "ArtifactWeave base URL without /artifactory."
}

variable "access_token" {
  type        = string
  sensitive   = true
  description = "Platform administrator access token."
}

variable "repository_prefix" {
  type        = string
  default     = "managed-replication"
  description = "Prefix for the local source and remote cache repository keys."
}

variable "target_url" {
  type        = string
  description = "Absolute target repository URL stored in the push configuration."
}

variable "upstream_url" {
  type        = string
  description = "Absolute upstream URL stored in the remote repository and pull configuration."
}

variable "proxy_key" {
  type        = string
  description = "Existing descriptor-managed proxy referenced by the push configuration."
}

variable "target_password" {
  type        = string
  sensitive   = true
  description = "Push authentication secret; Terraform retains its sensitive input in state."
}

variable "upstream_password" {
  type        = string
  sensitive   = true
  ephemeral   = true
  description = "Remote write-only upstream secret; never retained in Terraform state."
}

variable "revision" {
  type        = number
  default     = 1
  description = "Configuration revision; revision 2 rotates secrets and changes control fields."
  validation {
    condition     = contains([1, 2], var.revision)
    error_message = "Use revision 1 or 2."
  }
}
