variable "region" {
  description = "Region of the primary bucket, which artifactweave writes to (the bootstrap's region)."
  type        = string
  default     = "eu-west-2"
}

variable "replica_region" {
  description = "Region of the replica bucket S3 replicates into."
  type        = string
  default     = "eu-west-1"

  validation {
    condition     = var.replica_region != var.region
    error_message = "replica_region must differ from region: this scenario is cross-region replication."
  }
}

variable "name" {
  description = "Prefix for the bucket names. It must start with artifactweave-, the only buckets the deployer may manage, and leave room for the suffixes within S3's 63 characters; scenario runs pass artifactweave-awscn-<instance>."
  type        = string
  default     = "artifactweave-geo"

  validation {
    condition     = startswith(var.name, "artifactweave-") && length(var.name) <= 52
    error_message = "name must start with artifactweave- and be at most 52 characters, so <name>-<8 hex>-r fits S3's 63."
  }
}

variable "store_prefix" {
  description = "Key prefix artifactweave writes under (blob.s3.prefix). Keys under <store_prefix>sha256/ replicate."
  type        = string
  default     = "local/"
}

variable "replication_time_control" {
  description = "S3 Replication Time Control: 99.9% of objects replicated within 15 minutes, with replication metrics, billed per GB replicated. Off by default."
  type        = bool
  default     = false
}

variable "conformance_role_name" {
  description = "Name of the data-plane role aws/bootstrap created; the bucket policies grant it both buckets' objects."
  type        = string
  default     = "artifactweave-conformance"
}

variable "app_role_arn" {
  description = "ARN of the role artifactweave runs as, which the bucket policies grant. Empty looks up conformance_role_name; scenario runs pass the deployer."
  type        = string
  default     = ""
}

variable "scenario_role_path" {
  description = "IAM path for the replication role: aws/bootstrap's scenario path, the only one the deployer may create roles under."
  type        = string
  default     = "/artifactweave-scenarios/"
}

variable "boundary_policy_name" {
  description = "Name of aws/bootstrap's permissions boundary (under /artifactweave/), which every role a scenario creates must carry."
  type        = string
  default     = "artifactweave-scenario-boundary"
}

variable "scenario_labels" {
  description = "Tags for the buckets and the replication role. Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches."
  type        = map(string)
  default     = {}
}
