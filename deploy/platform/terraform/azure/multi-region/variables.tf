variable "subscription_id" {
  description = "Subscription that holds the storage accounts. Nothing is created anywhere else."
  type        = string
  default     = "c33dbc35-f86d-4b7c-a81e-2a33eb3f7946"
}

variable "primary_location" {
  description = "Region of the first member, and this instance's zone (blob.mirror.zone): reads prefer it."
  type        = string
  default     = "uksouth"
}

variable "secondary_location" {
  description = "Region of the second member."
  type        = string
  default     = "ukwest"

  validation {
    condition     = var.secondary_location != var.primary_location
    error_message = "secondary_location must differ from primary_location."
  }
}

variable "name" {
  description = "Prefix for the resource group this root creates when resource_group_name is empty."
  type        = string
  default     = "artifactweave-mr"
}

variable "container" {
  description = "Blob container in each account (blob.stores.<name>.azure.container)."
  type        = string
  default     = "artifactweave"
}

variable "store_prefix" {
  description = "Blob prefix artifactweave writes under in each account (blob.stores.<name>.azure.prefix)."
  type        = string
  default     = "local/"
}

variable "write_quorum" {
  description = "Copies stored before an upload succeeds (blob.mirror.write_quorum). 1 keeps uploads going with a region down; the second copy follows in the background or through repair."
  type        = number
  default     = 1

  validation {
    condition     = var.write_quorum >= 1 && var.write_quorum <= 2
    error_message = "write_quorum must be 1 or 2, the members there are."
  }
}

variable "bootstrap_resource_group" {
  description = "Resource group of azure/bootstrap, which holds the conformance identity."
  type        = string
  default     = "artifactweave-bootstrap-rg"
}

variable "conformance_identity" {
  description = "Name of the data-plane identity azure/bootstrap created; it gets blob rights on both accounts. Empty grants nothing beyond whoever applies (scenario runs, where the deployer applies and runs artifactweave)."
  type        = string
  default     = "artifactweave-conformance"
}

variable "resource_group_name" {
  description = "An existing resource group to create the accounts in. Empty creates <name>-rg; scenario runs pass azure/bootstrap's scenarios group, where the deployer's rights end."
  type        = string
  default     = ""
}

variable "scenario_labels" {
  description = "Tags for both accounts (and the resource group when this root creates it). Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches."
  type        = map(string)
  default     = {}
}

variable "register_resource_providers" {
  description = "Register Microsoft.Storage and Microsoft.ManagedIdentity. Scenario runs pass false: the deployer has no subscription-level rights, and the bootstrap's subscription has them registered already."
  type        = bool
  default     = true
}
