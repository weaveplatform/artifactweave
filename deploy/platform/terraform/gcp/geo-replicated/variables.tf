variable "project_id" {
  description = "Project that holds the bucket. Nothing is created anywhere else."
  type        = string
}

variable "name" {
  description = "Start of the bucket's name. The bucket is <name>-<8 hex>, which must fit Cloud Storage's 63 characters."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]*$", var.name))
    error_message = "name must be lowercase letters, digits and hyphens, starting with a letter or digit."
  }
}

variable "location" {
  description = "A predefined dual-region such as EUR4 (Finland and the Netherlands), or a multi-region such as EU together with data_locations for a configurable dual-region."
  type        = string

  # A region name (europe-west2) is a one-region bucket: nothing replicates.
  validation {
    condition     = !can(regex("^[a-z]+-[a-z]+[0-9]+$", lower(var.location)))
    error_message = "location must be a dual-region (EUR4) or a multi-region (EU, with data_locations); a single region is not replicated."
  }
}

variable "data_locations" {
  description = "The two regions of a configurable dual-region inside location, for example [\"europe-west1\", \"europe-west4\"] with location EU. Empty uses the predefined dual-region in location."
  type        = list(string)
  default     = []

  validation {
    condition     = length(var.data_locations) == 0 || length(var.data_locations) == 2
    error_message = "A configurable dual-region names exactly two regions."
  }
}

variable "turbo_replication" {
  description = "Turbo replication: a 15-minute recovery point objective instead of the default (99.9% of new objects within an hour), billed per GB written. Dual-regions only."
  type        = bool
  default     = false
}

variable "app_service_account" {
  description = "Email of the service account artifactweave runs as. It gets object rights on the bucket, and signs download URLs (blob.gcs.google_access_id)."
  type        = string

  validation {
    condition     = can(regex("^[^@]+@[^@]+\\.gserviceaccount\\.com$", var.app_service_account))
    error_message = "app_service_account must be a service account email."
  }
}

variable "store_prefix" {
  description = "Object prefix artifactweave writes under (blob.gcs.prefix). Empty writes at the bucket's root."
  type        = string
  default     = ""
}

variable "expiring_prefixes" {
  description = "Object prefixes, from the bucket's root, whose objects expire after the given number of days, for example { \"scratch/\" = 1 }."
  type        = map(number)
  default     = {}
}

variable "force_destroy" {
  description = "Let terraform destroy delete the bucket with every object still in it. Off, a destroy fails while the bucket holds anything."
  type        = bool
  default     = false
}

variable "labels" {
  description = "Labels for the bucket."
  type        = map(string)
  default     = {}
}
