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
  description = "Blob prefix the local run writes under (blob.azure.prefix)."
  type        = string
  default     = "local/"
}

variable "bootstrap_resource_group" {
  description = "Resource group of azure/bootstrap, which holds the conformance identity."
  type        = string
  default     = "artifactweave-bootstrap-rg"
}

variable "conformance_identity" {
  description = "Name of the data-plane identity azure/bootstrap created; it gets blob rights on this account. Empty grants nothing beyond whoever applies (scenario runs, where the deployer applies and runs artifactweave)."
  type        = string
  default     = "artifactweave-conformance"
}

variable "resource_group_name" {
  description = "An existing resource group to create the account in. Empty creates <name>-rg; scenario runs pass azure/bootstrap's scenarios group, where the deployer's rights end."
  type        = string
  default     = ""
}

variable "scenario_labels" {
  description = "Tags for the account (and the resource group when this root creates it). Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches."
  type        = map(string)
  default     = {}
}

variable "register_resource_providers" {
  description = "Register Microsoft.Storage and Microsoft.ManagedIdentity. Scenario runs pass false: the deployer has no subscription-level rights, and the bootstrap's subscription has them registered already."
  type        = bool
  default     = true
}
