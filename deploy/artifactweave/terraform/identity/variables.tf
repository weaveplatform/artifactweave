variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory, for example https://artifacts.example.com."
}

variable "access_token" {
  type        = string
  description = "Admin access token; both providers send it as a Bearer token."
  sensitive   = true
}

variable "user_password" {
  type        = string
  description = "Password for the developer and CI users. It must satisfy the server's password policy."
  sensitive   = true
}

variable "keypair_private_key" {
  type        = string
  description = "PEM RSA private key for the signing key pair, in PKCS#1 form (BEGIN RSA PRIVATE KEY): the provider accepts no other."
  sensitive   = true
}

variable "keypair_public_key" {
  type        = string
  description = "PEM public key matching keypair_private_key."
}

variable "repository_key" {
  type        = string
  default     = "team-local"
  description = "Key of the local generic repository the team permission covers."
}

variable "developers_group" {
  type        = string
  default     = "developers"
  description = "Group that reads and deploys to the repository, written through Access v2 (platform_group)."
}

variable "auditors_group" {
  type        = string
  default     = "auditors"
  description = "Read-only group, written through the legacy security API (artifactory_group)."
}

variable "developer_user" {
  type        = string
  default     = "developer"
  description = "User in both groups, with read, deploy and annotate on the repository."
}

variable "ci_user" {
  type        = string
  default     = "ci"
  description = "Managed user for CI, in the developers group; the scoped token is issued to it."
}

variable "email_domain" {
  type        = string
  default     = "example.com"
  description = "Domain of the users' email addresses, which are <user>@<email_domain>."
}

variable "permission_name" {
  type        = string
  default     = "team"
  description = "Name of the permission over the repository."
}

variable "keypair_name" {
  type        = string
  default     = "signing"
  description = "Name and alias of the signing key pair."
}
