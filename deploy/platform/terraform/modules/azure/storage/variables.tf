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

variable "replication_type" {
  description = "How Azure keeps copies of the account's data: LRS or ZRS within the region, GRS, RAGRS, GZRS or RAGZRS in its paired region too."
  type        = string
  default     = "LRS"

  validation {
    condition     = contains(["LRS", "ZRS", "GRS", "RAGRS", "GZRS", "RAGZRS"], var.replication_type)
    error_message = "replication_type must be one of LRS, ZRS, GRS, RAGRS, GZRS and RAGZRS."
  }
}

variable "container" {
  description = "Blob container artifactweave writes to (blob.azure.container)."
  type        = string
  default     = "artifactweave"

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.container)) && !strcontains(var.container, "--")
    error_message = "container must be 3 to 63 lowercase letters, digits and single hyphens, starting and ending with a letter or digit."
  }
}

variable "store_prefix" {
  description = "Blob prefix artifactweave writes under in the container (blob.azure.prefix), normalised as the backend does: no leading slash, one trailing slash. Its _uploads/ blobs expire after two days."
  type        = string
  default     = ""
}

variable "principals" {
  description = "Principals that get Storage Blob Data Contributor on the account, by a static name (app, operator), so the plan does not wait for their object IDs. principal_type (User, Group or ServicePrincipal) is optional; naming it spares Azure a directory lookup, which a newly created identity may not answer yet."
  type = map(object({
    principal_id   = string
    principal_type = optional(string)
  }))

  validation {
    condition     = alltrue([for p in values(var.principals) : can(regex("^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$", p.principal_id))])
    error_message = "each principal_id must be an object ID (a GUID)."
  }

  validation {
    condition     = alltrue([for p in values(var.principals) : p.principal_type == null || contains(["User", "Group", "ServicePrincipal"], coalesce(p.principal_type, "User"))])
    error_message = "each principal_type must be User, Group or ServicePrincipal, or left out."
  }
}

variable "expiring_prefixes" {
  description = "Further blob prefixes, from the container's root, whose blobs expire after the given number of days, for example { \"scratch/\" = 1 }."
  type        = map(number)
  default     = {}

  validation {
    condition     = alltrue([for days in values(var.expiring_prefixes) : days >= 1 && floor(days) == days])
    error_message = "each expiring prefix needs a whole number of days, at least 1."
  }
}

variable "tags" {
  description = "Tags for the storage account, the one resource here that takes them."
  type        = map(string)
  default     = {}
}
