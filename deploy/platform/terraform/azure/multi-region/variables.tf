variable "subscription_id" {
  description = "Subscription that holds the storage accounts. Nothing is created anywhere else."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$", var.subscription_id))
    error_message = "subscription_id must be a subscription ID (a GUID)."
  }
}

variable "name" {
  description = "Start of every resource name. Storage accounts take its letters and digits, lowercased and cut to fit 24 characters with a random suffix and the location's first three letters; the resource group this root creates is <name>-rg."
  type        = string

  validation {
    condition     = can(regex("^[A-Za-z0-9][A-Za-z0-9-]{0,62}$", var.name))
    error_message = "name must be 1 to 63 letters, digits and hyphens, starting with a letter or digit."
  }
}

variable "locations" {
  description = "Regions of the mirror's members, one storage account each, in order. The first is this instance's zone (blob.mirror.zone): reads prefer it, and the resource group this root creates is there."
  type        = list(string)

  validation {
    condition     = length(var.locations) >= 2 && length(distinct(var.locations)) == length(var.locations)
    error_message = "locations must name at least two different regions."
  }
}

variable "app_principal_id" {
  description = "Object ID of the identity artifactweave runs as (a managed identity, or a service principal). It gets Storage Blob Data Contributor on every account."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$", var.app_principal_id))
    error_message = "app_principal_id must be an object ID (a GUID)."
  }
}

variable "additional_principals" {
  description = "Further principals that get Storage Blob Data Contributor on every account, by a static name: an operator who runs artifactweave or mints user delegation SAS from a workstation, for one. principal_type (User, Group or ServicePrincipal) is optional."
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
  description = "An existing resource group to create the accounts in. Null creates <name>-rg in the first location."
  type        = string
  default     = null
}

variable "container" {
  description = "Blob container in each account (blob.stores.<name>.azure.container)."
  type        = string
  default     = "artifactweave"
}

variable "store_prefix" {
  description = "Blob prefix artifactweave writes under in each container (blob.stores.<name>.azure.prefix). Empty writes at the container's root."
  type        = string
  default     = ""
}

variable "expiring_prefixes" {
  description = "Blob prefixes, from each container's root, whose blobs expire after the given number of days, for example { \"scratch/\" = 1 }."
  type        = map(number)
  default     = {}
}

variable "tags" {
  description = "Tags for every storage account, and for the resource group when this root creates it."
  type        = map(string)
  default     = {}
}

variable "write_quorum" {
  description = "Copies stored before an upload succeeds (blob.mirror.write_quorum). 1 keeps uploads going with a region down; the other copies follow in the background or through repair."
  type        = number
  default     = 1

  validation {
    condition     = var.write_quorum >= 1 && var.write_quorum <= length(var.locations)
    error_message = "write_quorum must be at least 1 and at most the number of locations."
  }
}
