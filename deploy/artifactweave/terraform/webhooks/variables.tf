variable "url" {
  description = "ArtifactWeave platform URL."
  type        = string
}

variable "access_token" {
  description = "Platform administrator access token used to manage subscriptions."
  type        = string
  sensitive   = true
}

variable "receiver_url" {
  description = "Webhook receiver URL stored in each handler; this fixture qualifies administration only."
  type        = string
}

variable "revision" {
  description = "Configuration revision used to exercise handler and criteria updates."
  type        = number
  default     = 1
}

variable "handler_secret" {
  description = "Write-only fixture secret for standard and custom handlers."
  type        = string
  sensitive   = true
}
