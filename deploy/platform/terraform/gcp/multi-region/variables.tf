variable "project_id" {
  description = "Project that holds the buckets (the bootstrap's lab project)."
  type        = string
  default     = "project-0c0b2d7a-9ad7-4221-9a8"
}

variable "primary_region" {
  description = "Region of the first member, and this instance's zone (blob.mirror.zone): reads prefer it."
  type        = string
  default     = "europe-west2"
}

variable "secondary_region" {
  description = "Region of the second member."
  type        = string
  default     = "europe-west1"

  validation {
    condition     = var.secondary_region != var.primary_region
    error_message = "secondary_region must differ from primary_region."
  }
}

variable "name" {
  description = "Prefix for the bucket names; each is <name>-<suffix>-<region>."
  type        = string
  default     = "artifactweave-mr"
}

variable "store_prefix" {
  description = "Object prefix artifactweave writes under in each bucket (blob.stores.<name>.gcs.prefix)."
  type        = string
  default     = "local/"
}

variable "write_quorum" {
  description = "Copies stored before an upload succeeds (blob.mirror.write_quorum). 1 keeps uploads going with a region down; the second copy follows in the background or through repair."
  type        = number
  default     = 1

  validation {
    condition     = var.write_quorum >= 1 && var.write_quorum <= 2
    error_message = "write_quorum must be 1 or 2, the members there are."
  }
}

variable "conformance_service_account" {
  description = "Account ID of the data-plane service account gcp/bootstrap created; it gets object rights on both buckets."
  type        = string
  default     = "artifactweave-conformance"
}

variable "app_service_account" {
  description = "Email of the service account artifactweave runs as and that gets object rights on both buckets. Empty uses conformance_service_account; scenario runs pass the deployer."
  type        = string
  default     = ""
}

variable "scenario_labels" {
  description = "Labels for both buckets. Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches."
  type        = map(string)
  default     = {}
}
