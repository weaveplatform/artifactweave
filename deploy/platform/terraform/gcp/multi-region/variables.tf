variable "project_id" {
  description = "Project that holds the buckets. Nothing is created anywhere else."
  type        = string
}

variable "name" {
  description = "Start of every bucket name. Each bucket is <name>-<8 hex>-<location>, which must fit Cloud Storage's 63 characters."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]*$", var.name))
    error_message = "name must be lowercase letters, digits and hyphens, starting with a letter or digit."
  }
}

variable "locations" {
  description = "Regions of the mirror's members, one bucket each, in order. The first is this instance's zone (blob.mirror.zone): reads prefer it."
  type        = list(string)

  validation {
    condition     = length(var.locations) >= 2 && length(distinct(var.locations)) == length(var.locations)
    error_message = "locations must name at least two different regions."
  }
}

variable "app_service_account" {
  description = "Email of the service account artifactweave runs as. It gets object rights on every bucket, and signs download URLs (blob.stores.<name>.gcs.google_access_id)."
  type        = string

  validation {
    condition     = can(regex("^[^@]+@[^@]+\\.gserviceaccount\\.com$", var.app_service_account))
    error_message = "app_service_account must be a service account email."
  }
}

variable "store_prefix" {
  description = "Object prefix artifactweave writes under in each bucket (blob.stores.<name>.gcs.prefix). Empty writes at the bucket's root."
  type        = string
  default     = ""
}

variable "expiring_prefixes" {
  description = "Object prefixes, from each bucket's root, whose objects expire after the given number of days, for example { \"scratch/\" = 1 }."
  type        = map(number)
  default     = {}
}

variable "force_destroy" {
  description = "Let terraform destroy delete the buckets with every object still in them. Off, a destroy fails while a bucket holds anything."
  type        = bool
  default     = false
}

variable "labels" {
  description = "Labels for every bucket."
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
