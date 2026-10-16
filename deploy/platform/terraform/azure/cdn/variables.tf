variable "subscription_id" {
  description = "The subscription to create everything in, passed to the azurerm provider so a CLI login that points elsewhere cannot create anything there."
  type        = string
}

variable "name" {
  description = "Start of every name: the Front Door profile and endpoint, the resource group (<name>-rg) when resource_group_name is null, and up to 15 letters and digits of the storage account's."
  type        = string

  # The Front Door endpoint's name is the shortest allowed, at 46 characters.
  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9-]{0,44}[a-z0-9])?$", var.name))
    error_message = "name must be 1 to 46 lowercase letters, digits and hyphens, starting and ending with a letter or digit."
  }
}

variable "location" {
  description = "Region of the storage account, for example uksouth. Front Door itself is global."
  type        = string
}

variable "app_principal_id" {
  description = "Object ID of the identity artifactweave runs as (its managed identity's principal ID, or a service principal's object ID). It gets Storage Blob Data Contributor on the account."
  type        = string
}

variable "additional_principals" {
  description = "Further principals that get Storage Blob Data Contributor on the account, by a static name, for example an operator who runs artifactweave locally."
  type = map(object({
    principal_id   = string
    principal_type = optional(string)
  }))
  default = {}
}

variable "resource_group_name" {
  description = "An existing resource group to create everything in. Null creates <name>-rg in location."
  type        = string
  default     = null
}

variable "app_ip_ranges" {
  description = "Public IPv4 addresses or CIDR ranges (/30 or wider) artifactweave reaches the account from. With these and app_subnet_ids both empty, the account's public endpoint is off and artifactweave needs a private endpoint of its own. Azure applies IP rules only to requests from other regions."
  type        = list(string)
  default     = []
}

variable "app_subnet_ids" {
  description = "Subnet IDs, with the Microsoft.Storage service endpoint, that artifactweave reaches the account from: how a host in the account's own region gets through."
  type        = list(string)
  default     = []
}

variable "redirect_ttl_minutes" {
  description = "How long a signed URL lives, in minutes: artifactweave's download.redirect.ttl, and how long a Front Door edge keeps a copy. A URL is then served at most this long after its SAS expires (decision 0029)."
  type        = number
  default     = 5
}

variable "private_link_location" {
  description = "Region where Front Door's private endpoint to the account terminates; it must be one where Front Door offers Private Link origins. Null uses location."
  type        = string
  default     = null
}

variable "domain_name" {
  description = "A host of your own for the CDN, such as cdn.example.com, in the Azure DNS zone dns_zone_id: a sibling of artifactweave's host, never a subdomain of it. Front Door validates it and issues its certificate asynchronously, which can take an hour. Null serves from the endpoint's own *.azurefd.net host."
  type        = string
  default     = null
}

variable "dns_zone_id" {
  description = "Resource ID of the Azure DNS zone holding domain_name. Set it with domain_name, and only then."
  type        = string
  default     = null
}

variable "container" {
  description = "Blob container artifactweave writes to (blob.azure.container)."
  type        = string
  default     = "artifactweave"
}

variable "store_prefix" {
  description = "Blob prefix artifactweave writes under (blob.azure.prefix). Front Door serves only the committed blobs under it."
  type        = string
  default     = ""
}

variable "expiring_prefixes" {
  description = "Blob prefixes, from the container's root, whose blobs expire after the given number of days, for example { \"scratch/\" = 1 }."
  type        = map(number)
  default     = {}
}

variable "tags" {
  description = "Tags for the resource group, the storage account, the Front Door profile and endpoint, and the DNS records."
  type        = map(string)
  default     = {}
}
