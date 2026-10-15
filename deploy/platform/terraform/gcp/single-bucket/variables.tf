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
  description = "Object prefix the local run writes under (blob.gcs.prefix)."
  type        = string
  default     = "local/"
}

variable "conformance_service_account" {
  description = "Account ID of the data-plane service account gcp/bootstrap created; it gets object rights on this bucket."
  type        = string
  default     = "artifactweave-conformance"
}

variable "app_service_account" {
  description = "Email of the service account artifactweave runs as and that gets object rights on the bucket. Empty uses conformance_service_account; scenario runs pass the deployer."
  type        = string
  default     = ""
}

variable "scenario_labels" {
  description = "Labels for the bucket. Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches."
  type        = map(string)
  default     = {}
}
