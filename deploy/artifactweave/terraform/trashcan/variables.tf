variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory."
}
variable "access_token" {
  type        = string
  sensitive   = true
  description = "Admin access token, sent as a Bearer token."
}
variable "enabled" {
  type        = bool
  default     = true
  description = "Whether deleted items go to the trash can. When false, a delete is final."
}
variable "retention_period_days" {
  type        = number
  default     = 14
  description = "Days a trashed item is kept before a garbage collection pass purges it; 0 purges at the next pass."
  validation {
    condition     = var.retention_period_days >= 0 && floor(var.retention_period_days) == var.retention_period_days
    error_message = "retention_period_days must be a whole number of days, 0 or more."
  }
}
