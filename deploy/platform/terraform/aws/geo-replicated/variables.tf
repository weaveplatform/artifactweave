variable "name" {
  description = "Start of every name: the buckets are <name>-<8 hex> and <name>-<8 hex>-r, within S3's 63 characters, and the replication role <name, cut to 40 characters>-<8 hex>-repl."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{0,51}$", var.name))
    error_message = "name must be 1 to 52 lowercase letters, digits and hyphens, starting with a letter or digit, so <name>-<8 hex>-r fits S3's 63 characters."
  }
}

variable "region" {
  description = "Region of the primary bucket, where artifactweave writes and signs download URLs (blob.s3.region)."
  type        = string
}

variable "replica_region" {
  description = "Region of the replica bucket, where S3 copies the committed blobs."
  type        = string

  validation {
    condition     = var.replica_region != var.region
    error_message = "replica_region must differ from region: this scenario is cross-region replication."
  }
}

variable "app_role_arn" {
  description = "ARN of the IAM role artifactweave runs as (its instance profile's or workload's role). Both bucket policies grant it their objects: the primary's in normal running, the replica's after a failover."
  type        = string

  validation {
    condition     = can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:role/", var.app_role_arn))
    error_message = "app_role_arn must be an IAM role ARN."
  }
}

variable "store_prefix" {
  description = "Key prefix artifactweave writes under (blob.s3.prefix). Keys under <store_prefix>sha256/, the committed blobs, replicate."
  type        = string
  default     = ""
}

variable "expiring_prefixes" {
  description = "Key prefixes, from each bucket's root, whose objects expire after the given number of days, for example { \"scratch/\" = 1 }."
  type        = map(number)
  default     = {}
}

variable "replication_time_control" {
  description = "S3 Replication Time Control: 99.9% of objects replicated within 15 minutes, with replication metrics, billed per GB replicated."
  type        = bool
  default     = false
}

variable "force_destroy" {
  description = "Let terraform destroy delete the buckets with every object version still in them. Off, a destroy fails while a bucket holds anything."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags for both buckets and the replication role."
  type        = map(string)
  default     = {}
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
}
