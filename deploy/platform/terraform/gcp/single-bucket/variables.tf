variable "project_id" {
  description = "Project that holds the acceptance bucket (the bootstrap's lab project)."
  type        = string
  default     = "project-0c0b2d7a-9ad7-4221-9a8"
}

variable "region" {
  description = "Location of the bucket."
  type        = string
  default     = "europe-west2"
}

variable "name" {
  description = "Prefix for the bucket name."
  type        = string
  default     = "artifactweave-acc"
}

variable "store_prefix" {
  description = "Object prefix the local container writes under (blob.gcs.prefix)."
  type        = string
  default     = "local/"
}

variable "conformance_service_account" {
  description = "Account ID of the data-plane service account gcp/bootstrap created; it gets object rights on this bucket."
  type        = string
  default     = "artifactweave-conformance"
}
