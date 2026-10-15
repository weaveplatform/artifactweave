variable "name" {
  description = "Bucket name: 3 to 63 lowercase letters, digits and hyphens, globally unique. The deployer may only manage buckets starting artifactweave-."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.name))
    error_message = "name must be 3 to 63 lowercase letters, digits and hyphens, starting and ending with a letter or digit."
  }
}

variable "region" {
  description = "Region of the bucket, for example eu-west-2."
  type        = string
}

variable "store_prefix" {
  description = "Key prefix artifactweave writes under (blob.s3.prefix); its _uploads/ parts expire after two days."
  type        = string
}

variable "app_role_arn" {
  description = "ARN of the role artifactweave runs as, which the bucket policy grants this bucket's objects."
  type        = string
}

variable "tags" {
  description = "Tags for the bucket. Scenario runs set artifactweave-scenario, instance and expires-at, which reap matches."
  type        = map(string)
  default     = {}
}
