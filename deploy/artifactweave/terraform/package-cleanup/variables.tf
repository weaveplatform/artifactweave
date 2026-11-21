variable "url" {
  type        = string
  description = "ArtifactWeave base URL without /artifactory."
}

variable "access_token" {
  type        = string
  sensitive   = true
  description = "Administrator token for this isolated provider fixture."
}

variable "repository_prefix" {
  type        = string
  default     = "tf-p11-cleanup"
  description = "Namespace reserved for the cleanup policy and its Maven repository."
}

variable "revision" {
  type        = number
  default     = 1
  description = "Revision 2 updates configuration and enables the policy separately."
  validation {
    condition     = contains([1, 2], var.revision)
    error_message = "Use revision 1 or 2."
  }
}
