variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory, for example https://artifacts.example.com."
}

variable "access_token" {
  type        = string
  description = "Admin access token, sent as a Bearer token."
  sensitive   = true
}

variable "cdn_repository_key" {
  type        = string
  default     = "generic-cdn"
  description = "Key of the local generic repository with cdnRedirect, whose downloads always go to the CDN, as on Artifactory's SaaS CDN."
}

variable "direct_repository_key" {
  type        = string
  default     = "generic-direct"
  description = "Key of the local generic repository with downloadRedirect, whose downloads go to the CDN where the store has one and to a signed storage URL otherwise, as on self-hosted Artifactory."
}
