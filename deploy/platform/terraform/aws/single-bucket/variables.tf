variable "name" {
  description = "Start of the bucket name, which is <name>-<8 hex> and must fit S3's 63 characters."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{0,53}$", var.name))
    error_message = "name must be 1 to 54 lowercase letters, digits and hyphens, starting with a letter or digit, so <name>-<8 hex> fits S3's 63 characters."
  }
}

variable "region" {
  description = "Region of the bucket (blob.s3.region)."
  type        = string
}

variable "app_role_arn" {
  description = "ARN of the IAM role artifactweave runs as (its instance profile's or workload's role). The bucket policy grants it the bucket's objects."
  type        = string

  validation {
    condition     = can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:role/", var.app_role_arn))
    error_message = "app_role_arn must be an IAM role ARN."
  }
}

variable "store_prefix" {
  description = "Key prefix artifactweave writes under (blob.s3.prefix). Empty writes at the bucket's root."
  type        = string
  default     = ""
}

variable "expiring_prefixes" {
  description = "Key prefixes, from the bucket's root, whose objects expire after the given number of days, for example { \"scratch/\" = 1 }."
  type        = map(number)
  default     = {}
}

variable "force_destroy" {
  description = "Let terraform destroy delete the bucket with every object still in it. Off, a destroy fails while the bucket holds anything."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags for the bucket."
  type        = map(string)
  default     = {}
}
