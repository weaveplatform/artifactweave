variable "name" {
  description = "Name of the backend bucket, URL map, SSL policy, target proxy, address, forwarding rule and the Certificate Manager pieces, which each kind keeps apart: 1 to 63 lowercase letters, digits and hyphens, starting with a letter."
  type        = string

  validation {
    condition     = can(regex("^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$", var.name))
    error_message = "name must be 1 to 63 lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen."
  }
}

variable "origin_bucket" {
  description = "Name of the bucket Cloud CDN serves, modules/gcp/storage's bucket output. The backend bucket serves its root, so artifactweave's blob.gcs.cdn.cloud_cdn.path_prefix stays empty."
  type        = string
}

variable "store_prefix" {
  description = "Object prefix artifactweave writes under (blob.gcs.prefix), as given to modules/gcp/storage. Cloud CDN's fill account may read only the committed blobs under it, <store_prefix>sha256/."
  type        = string
  default     = ""
}

variable "domain_name" {
  description = "The CDN's host, such as cdn.example.com, served with a Google-managed certificate. Make it a sibling of artifactweave's host, never a subdomain of it, so clients drop their credentials on the redirect; artifactweave refuses to start otherwise."
  type        = string

  validation {
    condition     = can(regex("^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$", var.domain_name))
    error_message = "domain_name must be a lowercase host name, such as cdn.example.com."
  }
}

variable "dns_zone" {
  description = "Name of the Cloud DNS managed zone, in the provider's project, that holds domain_name: the certificate's authorisation record and the host's A record go there. Null creates no records; add the two in dns_records wherever the domain is served."
  type        = string
  default     = null
}

variable "grant_cdn_fill" {
  description = "Let Cloud CDN's fill account, service-<project number>@cloud-cdn-fill.iam.gserviceaccount.com, read the committed blobs. Google creates that account when a project's first signed URL key is added, and the grant fails before then: apply, add the key with gcloud, then turn this on."
  type        = bool
  default     = false
}

variable "signed_url_cache_max_age_sec" {
  description = "How long, in seconds, Cloud CDN keeps a response to a signed URL fresh, whatever the bucket's headers say. Blobs never change, and the signature is checked on every request, cached or not."
  type        = number
  default     = 86400

  validation {
    condition     = var.signed_url_cache_max_age_sec >= 0 && var.signed_url_cache_max_age_sec <= 31536000 && floor(var.signed_url_cache_max_age_sec) == var.signed_url_cache_max_age_sec
    error_message = "signed_url_cache_max_age_sec must be a whole number of seconds from 0 to 31536000 (a year)."
  }
}

variable "labels" {
  description = "Labels for the address, the forwarding rule and the Certificate Manager pieces, the resources here that take them."
  type        = map(string)
  default     = {}
}
