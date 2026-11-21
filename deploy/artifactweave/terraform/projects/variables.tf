variable "url" {
  type        = string
  description = "ArtifactWeave base URL without /artifactory."
}

variable "access_token" {
  type        = string
  sensitive   = true
  description = "Platform administrator token for this isolated provider fixture."
}

variable "resource_prefix" {
  type        = string
  default     = "tfp11"
  description = "Short prefix reserved for this scenario's projects and resources."
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,9}$", var.resource_prefix))
    error_message = "Use 2 to 10 lowercase letters, digits or hyphens, starting with a letter."
  }
}

variable "user_password" {
  type        = string
  sensitive   = true
  description = "Fixture user password, retained only in isolated private Terraform state."
}

variable "revision" {
  type        = number
  default     = 1
  description = "Revision 2 changes membership, role/share authority and hard versus soft quota."
  validation {
    condition     = contains([1, 2], var.revision)
    error_message = "Use revision 1 or 2."
  }
}
