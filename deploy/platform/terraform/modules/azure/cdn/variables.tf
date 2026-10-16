variable "name" {
  description = "Name of the Front Door profile and its endpoint: 1 to 46 lowercase letters, digits and hyphens. The endpoint's host is <name>-<hash>.z01.azurefd.net, so the name need only be unique in the resource group."
  type        = string

  # An endpoint's name is the shortest Front Door allows, at 46 characters.
  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9-]{0,44}[a-z0-9])?$", var.name))
    error_message = "name must be 1 to 46 lowercase letters, digits and hyphens, starting and ending with a letter or digit."
  }
}

variable "resource_group_name" {
  description = "Resource group to create the profile in."
  type        = string
}

variable "origin" {
  description = "The storage account Front Door reads from over Private Link: modules/azure/storage's id and host outputs, and the account's location."
  type = object({
    id       = string
    host     = string
    location = string
  })

  validation {
    condition     = can(regex("(?i)^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft\\.Storage/storageAccounts/[a-z0-9]{3,24}$", var.origin.id))
    error_message = "origin.id must be a storage account's resource ID."
  }
}

variable "container" {
  description = "The container artifactweave writes to (blob.azure.container), as given to modules/azure/storage. The route serves only the committed blobs under it."
  type        = string
  default     = "artifactweave"
}

variable "store_prefix" {
  description = "Blob prefix artifactweave writes under (blob.azure.prefix), as given to modules/azure/storage. The route serves only the committed blobs, /<container>/<store_prefix>sha256/."
  type        = string
  default     = ""
}

variable "private_link_location" {
  description = "Region where Front Door's private endpoint to the account terminates; it must be one where Front Door offers Private Link origins. Null uses the account's location, which keeps the link closest to the origin."
  type        = string
  default     = null
}

variable "max_cache_age_minutes" {
  description = "How long an edge keeps a blob's response, in minutes. Set it to artifactweave's download.redirect.ttl: a signed URL then stops being served at most this long after its SAS expires (decision 0029)."
  type        = number
  default     = 5

  # Front Door's rules take durations below a day as HH:MM:SS.
  validation {
    condition     = var.max_cache_age_minutes >= 1 && var.max_cache_age_minutes <= 1439 && floor(var.max_cache_age_minutes) == var.max_cache_age_minutes
    error_message = "max_cache_age_minutes must be a whole number of minutes, from 1 to 1439."
  }
}

variable "domain_name" {
  description = "A host of your own for the endpoint, such as cdn.example.com, in the Azure DNS zone dns_zone_id, served with a Front Door managed certificate. Make it a sibling of artifactweave's host, never a subdomain of it, so clients drop their credentials on the redirect; artifactweave refuses to start otherwise. Null serves from the endpoint's own *.azurefd.net host."
  type        = string
  default     = null

  validation {
    condition     = var.domain_name == null || can(regex("^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$", var.domain_name))
    error_message = "domain_name must be a lowercase host name, such as cdn.example.com."
  }
}

variable "dns_zone_id" {
  description = "Resource ID of the Azure DNS zone holding domain_name, where the module writes the validation TXT record and the CNAME to the endpoint. Set it with domain_name, and only then."
  type        = string
  default     = null

  validation {
    condition     = (var.dns_zone_id == null) == (var.domain_name == null)
    error_message = "set dns_zone_id together with domain_name, and only then."
  }

  validation {
    condition = var.dns_zone_id == null || (
      can(regex("(?i)^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft\\.Network/dnsZones/[^/]+$", var.dns_zone_id)) &&
      endswith(coalesce(var.domain_name, "-"), ".${try(regex("[^/]+$", var.dns_zone_id), "")}")
    )
    error_message = "dns_zone_id must be an Azure DNS zone's resource ID, and domain_name a host inside that zone."
  }
}

variable "tags" {
  description = "Tags for the profile, the endpoint and the DNS records, the resources here that take them."
  type        = map(string)
  default     = {}
}
