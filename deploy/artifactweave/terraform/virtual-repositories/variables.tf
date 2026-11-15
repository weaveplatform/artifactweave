variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory."
}

variable "access_token" {
  type        = string
  sensitive   = true
  description = "Admin access token, sent as a Bearer token."
}

variable "repository_prefix" {
  type        = string
  default     = "managed-virtual"
  description = "Prefix of every repository key. A virtual is named prefix-type, and its members prefix-type-local and prefix-type-remote."
}

variable "upstream_url" {
  type        = string
  description = "Base URL the remote members proxy. The remote of each type takes this URL with /type appended. Creating a remote fetches nothing from it."
  validation {
    condition     = can(regex("^https?://", var.upstream_url))
    error_message = "Supply an HTTP(S) URL."
  }
}

variable "revision" {
  type        = number
  default     = 1
  description = "Configuration revision, 1 or 2. Revision 2 reverses every virtual's member order and changes one field of each."
  validation {
    condition     = contains([1, 2], var.revision)
    error_message = "Use revision 1 or 2."
  }
}

variable "gpg_primary_keypair_name" {
  type        = string
  default     = "managed-virtual-gpg-primary"
  description = "Name of the GPG key pair the Debian and RPM virtuals sign with first, and the Maven-family virtuals' key_pair."
}

variable "gpg_primary_private_key" {
  type        = string
  sensitive   = true
  description = "ASCII-armoured private half of the primary GPG key pair, without a passphrase."
}

variable "gpg_primary_public_key" {
  type        = string
  description = "ASCII-armoured public half of the primary GPG key pair."
}

variable "gpg_secondary_keypair_name" {
  type        = string
  default     = "managed-virtual-gpg-secondary"
  description = "Name of the GPG key pair the Debian and RPM virtuals name as their secondary pair."
}

variable "gpg_secondary_private_key" {
  type        = string
  sensitive   = true
  description = "ASCII-armoured private half of the secondary GPG key pair, without a passphrase."
}

variable "gpg_secondary_public_key" {
  type        = string
  description = "ASCII-armoured public half of the secondary GPG key pair."
}

variable "rsa_keypair_name" {
  type        = string
  default     = "managed-virtual-rsa"
  description = "Name of the RSA key pair the Alpine virtual and the Hex repositories sign with."
}

variable "rsa_private_key" {
  type        = string
  sensitive   = true
  description = "PEM private half of the RSA key pair, in PKCS#1 form (BEGIN RSA PRIVATE KEY), which the provider requires."
}

variable "rsa_public_key" {
  type        = string
  description = "PEM public half of the RSA key pair. The Hex remote also takes it as its upstream's public key."
}
