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

variable "bootstrap_resource_group" {
  description = "Resource group of azure/bootstrap, which holds the conformance identity."
  type        = string
  default     = "artifactweave-bootstrap-rg"
}

variable "conformance_identity" {
  description = "Name of the data-plane identity azure/bootstrap created; it gets blob rights on this account."
  type        = string
  default     = "artifactweave-conformance"
}
