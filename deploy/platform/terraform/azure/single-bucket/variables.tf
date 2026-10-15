variable "subscription_id" {
  description = "Subscription that holds the storage account. Nothing is created anywhere else."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$", var.subscription_id))
    error_message = "subscription_id must be a subscription ID (a GUID)."
  }
}

variable "name" {
  description = "Start of every resource name. The storage account takes its letters and digits, lowercased and cut to fit 24 characters with a random suffix and the location's first three letters; the resource group this root creates is <name>-rg."
  type        = string

  validation {
    condition     = can(regex("^[A-Za-z0-9][A-Za-z0-9-]{0,62}$", var.name))
    error_message = "name must be 1 to 63 letters, digits and hyphens, starting with a letter or digit."
  }
}

variable "location" {
  description = "Region of the storage account, and of the resource group this root creates."
  type        = string
}

variable "app_principal_id" {
  description = "Object ID of the identity artifactweave runs as (a managed identity, or a service principal). It gets Storage Blob Data Contributor on the account."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$", var.app_principal_id))
    error_message = "app_principal_id must be an object ID (a GUID)."
  }
}

variable "additional_principals" {
  description = "Further principals that get Storage Blob Data Contributor on the account, by a static name: an operator who runs artifactweave or mints user delegation SAS from a workstation, for one. principal_type (User, Group or ServicePrincipal) is optional."
  type = map(object({
    principal_id   = string
    principal_type = optional(string)
  }))
  default = {}

  validation {
    condition     = !contains(keys(var.additional_principals), "app")
    error_message = "additional_principals cannot use the name app, which is app_principal_id's."
  }
}

variable "resource_group_name" {
  description = "An existing resource group to create the account in. Null creates <name>-rg in location."
  type        = string
  default     = null
}

variable "container" {
  description = "Blob container in the account (blob.azure.container)."
  type        = string
  default     = "artifactweave"
}

variable "store_prefix" {
  description = "Blob prefix artifactweave writes under in the container (blob.azure.prefix). Empty writes at the container's root."
  type        = string
  default     = ""
}

variable "expiring_prefixes" {
  description = "Blob prefixes, from the container's root, whose blobs expire after the given number of days, for example { \"scratch/\" = 1 }."
  type        = map(number)
  default     = {}
}

variable "tags" {
  description = "Tags for the storage account, and for the resource group when this root creates it."
  type        = map(string)
  default     = {}
}
