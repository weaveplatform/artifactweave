variable "subscription_id" {
  description = "Subscription that holds the acceptance storage account. Nothing is created anywhere else."
  type        = string
  default     = "c33dbc35-f86d-4b7c-a81e-2a33eb3f7946"
}

variable "location" {
  description = "Region of the resource group and storage account."
  type        = string
  default     = "uksouth"
}

variable "name" {
  description = "Prefix for the resource names."
  type        = string
  default     = "artifactweave-acc"
}

variable "container" {
  description = "Blob container for blob.azure.container."
  type        = string
  default     = "artifactweave"
}

variable "store_prefix" {
  description = "Blob prefix the local container writes under (blob.azure.prefix)."
  type        = string
  default     = "local/"
}

variable "github_subject_prefix" {
  description = "The OIDC subject prefix of the repository whose Actions may act as the identity. This repository uses GitHub's immutable subject, which names the owner and repository with their IDs; read it with `gh api repos/{owner}/{repo}/actions/oidc/customization/sub --jq .sub_claim_prefix`."
  type        = string
  default     = "repo:weaveplatform@336817058/artifactweave@1415179278"
}

variable "github_environment" {
  description = "GitHub environment the workflow's azure job runs in. Azure matches the OIDC subject exactly, and an environment gives a subject independent of branch and event."
  type        = string
  default     = "cloud-conformance"
}
