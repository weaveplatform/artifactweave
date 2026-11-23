variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory."
}
variable "access_token" {
  type        = string
  sensitive   = true
  description = "Admin access token, sent as a Bearer token."
}
variable "enable_anonymous_access" {
  type        = bool
  default     = false
  description = "Whether requests without credentials act as the anonymous user."
}
variable "encryption_policy" {
  type        = string
  default     = "SUPPORTED"
  description = "Which internal passwords HTTP Basic accepts: SUPPORTED (clear-text or encrypted), REQUIRED (encrypted only, outside the web UI) or UNSUPPORTED (clear-text only)."
  validation {
    condition     = contains(["SUPPORTED", "REQUIRED", "UNSUPPORTED"], var.encryption_policy)
    error_message = "encryption_policy must be SUPPORTED, REQUIRED or UNSUPPORTED."
  }
}
