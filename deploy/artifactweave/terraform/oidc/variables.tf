variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory, for example https://artifacts.example.com."
}

variable "access_token" {
  type        = string
  description = "Admin access token, sent as a Bearer token."
  sensitive   = true
}

variable "github_organization" {
  type        = string
  description = "GitHub organisation whose Actions tokens are accepted, and whose main-branch builds act as ci_bot_user."
}

variable "ci_project_path" {
  type        = string
  description = "project_path claim pattern the generic CI provider's readers mapping matches, for example my-group/** on GitLab."
}

variable "github_provider_name" {
  type        = string
  default     = "github"
  description = "Name of the GitHub Actions OIDC provider."
}

variable "ci_provider_name" {
  type        = string
  default     = "ci"
  description = "Name of the generic OpenID Connect provider."
}

variable "ci_issuer_url" {
  type        = string
  default     = "https://ci.example.com"
  description = "Issuer URL of the generic CI system, such as GitLab or Jenkins."
}

variable "audience" {
  type        = string
  default     = "artifactweave"
  description = "Audience the ID tokens must carry."
}

variable "ci_bot_user" {
  type        = string
  default     = "ci-bot"
  description = "User the GitHub main-branch builds act as."
}

variable "readers_group" {
  type        = string
  default     = "readers"
  description = "Group every other generic CI build's token is limited to."
}
