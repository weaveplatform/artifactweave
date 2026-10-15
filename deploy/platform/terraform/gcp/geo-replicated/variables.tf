variable "project_id" {
  description = "Project that holds the bucket (the bootstrap's lab project)."
  type        = string
  default     = "project-0c0b2d7a-9ad7-4221-9a8"
}

variable "location" {
  description = "A predefined dual-region (EUR4: Finland and the Netherlands), or a multi-region such as EU together with data_locations for a configurable dual-region."
  type        = string
  default     = "EUR4"
}

variable "data_locations" {
  description = "The two regions of a configurable dual-region, for example [\"europe-west1\", \"europe-west4\"] with location EU. Empty uses the predefined dual-region in location."
  type        = list(string)
  default     = []

  validation {
    condition     = length(var.data_locations) == 0 || length(var.data_locations) == 2
    error_message = "A configurable dual-region names exactly two regions."
  }
}

variable "turbo_replication" {
  description = "Turbo replication: a 15-minute recovery point objective instead of the default (99.9% within an hour), billed per GB written. Dual-regions only."
  type        = bool
  default     = false
}

variable "name" {
  description = "Prefix for the bucket name."
  type        = string
  default     = "artifactweave-geo"
}

variable "store_prefix" {
  description = "Object prefix artifactweave writes under (blob.gcs.prefix)."
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
