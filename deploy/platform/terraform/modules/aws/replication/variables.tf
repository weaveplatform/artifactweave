variable "name" {
  description = "Name of the IAM role S3 assumes to replicate."
  type        = string

  validation {
    condition     = can(regex("^[A-Za-z0-9+=,.@_-]{1,64}$", var.name))
    error_message = "name must be 1 to 64 letters, digits and +=,.@_- characters, as an IAM role name is."
  }
}

variable "source_bucket" {
  description = "The bucket S3 replicates from, as modules/aws/storage outputs it: pass the module itself (module.primary). Replication is configured in its region."
  type = object({
    bucket    = string
    arn       = string
    region    = string
    versioned = bool
  })

  validation {
    condition     = var.source_bucket.versioned
    error_message = "source_bucket must be versioned: S3 replication needs versioning on both buckets."
  }
}

variable "destination_bucket" {
  description = "The bucket S3 replicates into, as modules/aws/storage outputs it: pass the module itself (module.replica)."
  type = object({
    bucket    = string
    arn       = string
    region    = string
    versioned = bool
  })

  validation {
    condition     = var.destination_bucket.versioned
    error_message = "destination_bucket must be versioned: S3 replication needs versioning on both buckets."
  }

  validation {
    condition     = var.destination_bucket.arn != var.source_bucket.arn
    error_message = "destination_bucket must be another bucket than source_bucket."
  }
}

variable "prefix" {
  description = "Key prefix, from the source bucket's root, under which objects replicate. Empty replicates the whole bucket."
  type        = string
  default     = ""
}

variable "replicate_deletes" {
  description = "Replicate delete markers. Off, an object deleted from the source stays in the destination, so a failover never misses an object a lagging delete would have taken."
  type        = bool
  default     = false
}

variable "replication_time_control" {
  description = "S3 Replication Time Control: 99.9% of objects replicated within 15 minutes, with replication metrics, billed per GB replicated."
  type        = bool
  default     = false
}

variable "storage_class" {
  description = "Storage class of the destination's copies."
  type        = string
  default     = "STANDARD"
}

variable "permissions_boundary_arn" {
  description = "Permissions boundary for the replication role, when the deployer may create roles only with one attached."
  type        = string
  default     = null
}

variable "iam_role_path" {
  description = "IAM path of the replication role, beginning and ending with /. Null leaves IAM's default, /."
  type        = string
  default     = null

  validation {
    condition     = var.iam_role_path == null || can(regex("^/([!-~]+/)?$", var.iam_role_path))
    error_message = "iam_role_path must begin and end with /."
  }
}

variable "tags" {
  description = "Tags for the replication role, the one resource here that takes them."
  type        = map(string)
  default     = {}
}
