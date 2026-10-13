variable "project_id" {
  description = "Project that holds the acceptance bucket and its service account."
  type        = string
  default     = "project-0c0b2d7a-9ad7-4221-9a8"
}

variable "region" {
  description = "Location of the bucket."
  type        = string
  default     = "europe-west2"
}

variable "name" {
  description = "Prefix for the bucket and service account names."
  type        = string
  default     = "artifactweave-acc"
}

variable "store_prefix" {
  description = "Object prefix the local container writes under (blob.gcs.prefix)."
  type        = string
  default     = "local/"
}

variable "github_repository" {
  description = "Repository whose Actions may act as the service account (cloud-conformance.yml)."
  type        = string
  default     = "weaveplatform/artifactweave"
}
