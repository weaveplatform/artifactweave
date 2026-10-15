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
  description = "Region of the bucket, for example europe-west2."
  type        = string
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
