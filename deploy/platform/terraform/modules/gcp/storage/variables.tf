variable "name" {
  description = "Bucket name, globally unique."
  type        = string
}

variable "location" {
  description = "Region of the bucket, for example europe-west2. A single region: the mirror, not the bucket, keeps the second copy."
  type        = string
}

variable "store_prefix" {
  description = "Object prefix artifactweave writes under (blob.gcs.prefix); its _uploads/ parts expire after two days."
  type        = string
}

variable "app_service_account" {
  description = "Email of the service account artifactweave runs as; it gets object rights on this bucket only."
  type        = string
}

variable "labels" {
  description = "Labels for the bucket. Scenario runs set artifactweave-scenario, instance and expires-at, which reap matches."
  type        = map(string)
  default     = {}
}
