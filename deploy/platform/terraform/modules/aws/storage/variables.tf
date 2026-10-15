variable "name" {
  description = "Bucket name: 3 to 63 lowercase letters, digits and hyphens, globally unique."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.name))
    error_message = "name must be 3 to 63 lowercase letters, digits and hyphens, starting and ending with a letter or digit."
  }
}

variable "region" {
  description = "Region of the bucket and of every resource that configures it."
  type        = string
}

variable "principals" {
  description = "ARNs of the IAM roles that get this bucket's objects and listing through the bucket policy, by a static name (app, for one), so the plan does not wait for the ARNs."
  type        = map(string)

  # An S3 bucket policy refuses a statement with no principal, so an empty
  # map would fail at apply rather than here.
  validation {
    condition     = length(var.principals) > 0
    error_message = "principals must name at least one role."
  }

  validation {
    condition     = alltrue([for arn in values(var.principals) : can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:role/", arn))])
    error_message = "each principal must be an IAM role ARN."
  }
}

variable "store_prefix" {
  description = "Key prefix artifactweave writes under (blob.s3.prefix), normalised as the backend does: no leading slash, one trailing slash. Its _uploads/ parts expire after two days."
  type        = string
  default     = ""
}

variable "versioning" {
  description = "Turn on object versioning, as S3 replication needs on both buckets. Noncurrent versions then expire after one day, so deleted blobs stop costing."
  type        = bool
  default     = false
}

variable "expiring_prefixes" {
  description = "Further key prefixes, from the bucket's root, whose objects expire after the given number of days, for example { \"scratch/\" = 1 }."
  type        = map(number)
  default     = {}

  validation {
    condition     = alltrue([for days in values(var.expiring_prefixes) : days >= 1 && floor(days) == days])
    error_message = "each expiring prefix needs a whole number of days, at least 1."
  }
}

variable "force_destroy" {
  description = "Let terraform destroy delete the bucket with every object (and version) still in it. Off, a destroy fails on a bucket that holds anything."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags for the bucket, the one resource here that takes them."
  type        = map(string)
  default     = {}
}
