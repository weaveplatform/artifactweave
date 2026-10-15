variable "region" {
  description = "Region of the bucket (the bootstrap's region)."
  type        = string
  default     = "eu-west-2"
}

variable "name" {
  description = "Prefix for the bucket name. It must start with artifactweave-, the only buckets the deployer may manage; scenario runs pass artifactweave-awscn-<instance>."
  type        = string
  default     = "artifactweave-acc"
}

variable "store_prefix" {
  description = "Key prefix the local container writes under (blob.s3.prefix)."
  type        = string
  default     = "local/"
}

variable "conformance_role_name" {
  description = "Name of the data-plane role aws/bootstrap created; the bucket policy grants it this bucket's objects."
  type        = string
  default     = "artifactweave-conformance"
}

variable "app_role_arn" {
  description = "ARN of the role artifactweave runs as, which the bucket policy grants. Empty looks up conformance_role_name; scenario runs pass the deployer, which runs artifactweave in the same job and cannot read the bootstrap's roles."
  type        = string
  default     = ""
}

variable "scenario_labels" {
  description = "Tags for the bucket. Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches."
  type        = map(string)
  default     = {}
}
