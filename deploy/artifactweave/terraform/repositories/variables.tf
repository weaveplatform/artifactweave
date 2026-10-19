variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory, for example https://artifacts.example.com."
}

variable "access_token" {
  type        = string
  description = "Admin access token, sent as a Bearer token."
  sensitive   = true
}

variable "repository_key" {
  type        = string
  default     = "generic-local"
  description = "Key of the local generic repository."
}

variable "item_path" {
  type        = string
  default     = "fixture.txt"
  description = "Path, inside the repository, of the file the properties are set on. The file must exist before the properties are applied: the provider cannot deploy content."
}

variable "properties" {
  type = map(list(string))
  default = {
    team = ["platform"]
    tier = ["gold", "silver"]
  }
  description = "Properties to set on item_path, each a key with its values."
}

variable "repository_properties" {
  type = map(list(string))
  default = {
    owner = ["platform"]
    stage = ["test", "release"]
  }
  description = "Properties to set on the generic repository itself, each a key with its values."
}

variable "with_properties" {
  type        = bool
  default     = true
  description = "Whether to set the properties on item_path. Apply with false first, deploy the file, then apply with true."
}

variable "npm_repository_key" {
  type        = string
  default     = "npm-local"
  description = "Key of the local npm repository."
}

variable "go_repository_key" {
  type        = string
  default     = "go-local"
  description = "Key of the local Go modules repository."
}

variable "maven_repository_key" {
  type        = string
  default     = "maven-releases-local"
  description = "Key of the local Maven release repository."
}

variable "gradle_repository_key" {
  type        = string
  default     = "gradle-snapshots-local"
  description = "Key of the local Gradle snapshot repository."
}

variable "pypi_repository_key" {
  type        = string
  default     = "pypi-local"
  description = "Key of the local PyPI repository."
}

variable "nuget_repository_key" {
  type        = string
  default     = "nuget-local"
  description = "Key of the local NuGet repository."
}

variable "helm_repository_key" {
  type        = string
  default     = "helm-local"
  description = "Key of the local Helm repository."
}

variable "helmoci_repository_key" {
  type        = string
  default     = "helmoci-local"
  description = "Key of the local Helm OCI repository."
}

variable "terraform_module_repository_key" {
  type        = string
  default     = "terraform-modules-local"
  description = "Key of the local Terraform module registry repository."
}

variable "terraform_provider_repository_key" {
  type        = string
  default     = "terraform-providers-local"
  description = "Key of the local Terraform provider registry repository."
}

variable "terraform_backend_repository_key" {
  type        = string
  default     = "terraform-backend-local"
  description = "Key of the local Terraform backend repository."
}

variable "gpg_keypair_name" {
  type        = string
  default     = "terraform-providers-gpg"
  description = "Name and alias of the GPG key pair the Terraform provider repository signs with."
}

variable "gpg_private_key" {
  type        = string
  description = "ASCII-armored GPG private key of that key pair, without a passphrase."
  sensitive   = true
}

variable "gpg_public_key" {
  type        = string
  description = "ASCII-armored GPG public key matching gpg_private_key."
}

variable "npm_virtual_repository_key" {
  type        = string
  default     = "npm"
  description = "Key of the virtual npm repository over the local npm repository."
}

variable "maven_virtual_repository_key" {
  type        = string
  default     = "maven"
  description = "Key of the virtual Maven repository, which has no members."
}
