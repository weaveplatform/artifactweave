variable "url" {
  type        = string
  description = "Base URL of the first artifactweave instance, without /artifactory."
}
variable "access_token" {
  type        = string
  sensitive   = true
  description = "Administrator access token for the first instance."
}
variable "member_urls" {
  type        = set(string)
  description = "Base URLs of three paired members, including the provider's own instance."
  validation {
    condition     = length(var.member_urls) == 3
    error_message = "The scenario requires exactly three members."
  }
}
variable "member_access_tokens" {
  type        = map(string)
  sensitive   = true
  default     = {}
  description = "Optional per-member tokens used only by the provider for cleanup_on_delete; they are not sent in repository JSON."
}
variable "name_prefix" {
  type        = string
  default     = "fed"
  description = "Prefix for the resource keys."
}
variable "description" {
  type        = string
  default     = "Three-member federation"
  description = "Description propagated to every member."
}
