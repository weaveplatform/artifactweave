variable "name" {
  description = "Bucket name: 3 to 63 lowercase letters, digits, hyphens and underscores, globally unique."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9_-]{1,61}[a-z0-9]$", var.name))
    error_message = "name must be 3 to 63 lowercase letters, digits, hyphens and underscores, starting and ending with a letter or digit."
  }
}

variable "location" {
  description = "Where the bucket keeps its data: a region (europe-west2), or with data_locations a multi-region (EU) for a configurable dual-region, or a predefined dual-region (EUR4)."
  type        = string
}

variable "data_locations" {
  description = "The two regions of a configurable dual-region bucket, inside location. Empty keeps the bucket where location alone says."
  type        = list(string)
  default     = []

  validation {
    condition     = length(var.data_locations) == 0 || length(var.data_locations) == 2
    error_message = "data_locations must name no region or exactly two."
  }
}

variable "rpo" {
  description = "Replication of a dual-region bucket: ASYNC_TURBO (turbo replication) or DEFAULT. Null leaves Google's default, and a single-region bucket takes none."
  type        = string
  default     = null

  validation {
    condition     = var.rpo == null || contains(["DEFAULT", "ASYNC_TURBO"], coalesce(var.rpo, "DEFAULT"))
    error_message = "rpo must be DEFAULT, ASYNC_TURBO or null."
  }
}

variable "principals" {
  description = "Emails of the service accounts that get object rights on this bucket, by a static name (app, for one), so the plan does not wait for the emails."
  type        = map(string)

  validation {
    condition     = alltrue([for email in values(var.principals) : can(regex("^[^@]+@[^@]+\\.gserviceaccount\\.com$", email))])
    error_message = "each principal must be a service account email."
  }
}

variable "store_prefix" {
  description = "Object prefix artifactweave writes under (blob.gcs.prefix), normalised as the backend does: no leading slash, one trailing slash. Its _uploads/ parts expire after two days."
  type        = string
  default     = ""
}

variable "expiring_prefixes" {
  description = "Further object prefixes, from the bucket's root, whose objects expire after the given number of days, for example { \"scratch/\" = 1 }."
  type        = map(number)
  default     = {}

  validation {
    condition     = alltrue([for days in values(var.expiring_prefixes) : days >= 1 && floor(days) == days])
    error_message = "each expiring prefix needs a whole number of days, at least 1."
  }
}

variable "force_destroy" {
  description = "Let terraform destroy delete the bucket with every object still in it. Off, a destroy fails on a bucket that holds anything."
  type        = bool
  default     = false
}

variable "labels" {
  description = "Labels for the bucket, the one resource here that takes them."
  type        = map(string)
  default     = {}
}
