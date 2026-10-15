variable "account_name" {
  description = "Storage account name: 3 to 24 lowercase letters and digits, globally unique."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9]{3,24}$", var.account_name))
    error_message = "account_name must be 3 to 24 lowercase letters and digits."
  }
}

variable "resource_group_name" {
  description = "Resource group to create the account in."
  type        = string
}

variable "location" {
  description = "Region of the account, for example uksouth."
  type        = string
}

variable "container" {
  description = "Blob container for blob.azure.container."
  type        = string
}

variable "store_prefix" {
  description = "Blob prefix artifactweave writes under (blob.azure.prefix); its _uploads/ blobs expire after two days."
  type        = string
}

variable "principals" {
  description = "Principals that get Storage Blob Data Contributor on the account, by a static name (operator, conformance) so the plan does not wait for their IDs."
  type        = map(string)
}

variable "tags" {
  description = "Tags for the account. Scenario runs set artifactweave-scenario, instance and expires-at, which reap matches."
  type        = map(string)
  default     = {}
}
