variable "primary_region" {
  description = "Region of the first member, and this instance's zone (blob.mirror.zone): reads prefer it."
  type        = string
  default     = "eu-west-2"
}

variable "secondary_region" {
  description = "Region of the second member."
  type        = string
  default     = "eu-west-1"

  validation {
    condition     = var.secondary_region != var.primary_region
    error_message = "secondary_region must differ from primary_region."
  }
}

variable "name" {
  description = "Prefix for the bucket names; each is <name>-<suffix>-<region code>. It must start with artifactweave-, the only buckets the deployer may manage; scenario runs pass artifactweave-awscn-<instance>."
  type        = string
  default     = "artifactweave-mr"
}

variable "store_prefix" {
  description = "Key prefix artifactweave writes under in each bucket (blob.stores.<name>.s3.prefix)."
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

variable "conformance_role_name" {
  description = "Name of the data-plane role aws/bootstrap created; both bucket policies grant it their objects."
  type        = string
  default     = "artifactweave-conformance"
}

variable "app_role_arn" {
  description = "ARN of the role artifactweave runs as, which both bucket policies grant. Empty looks up conformance_role_name; scenario runs pass the deployer, which runs artifactweave in the same job and cannot read the bootstrap's roles."
  type        = string
  default     = ""
}

variable "scenario_labels" {
  description = "Tags for both buckets. Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches."
  type        = map(string)
  default     = {}
}
