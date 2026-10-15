variable "name" {
  description = "Start of every bucket name. Each bucket is <name>-<8 hex>-<region code> (eu-central-1 becomes euc1), which must fit S3's 63 characters."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]*$", var.name))
    error_message = "name must be lowercase letters, digits and hyphens, starting with a letter or digit."
  }
}

variable "regions" {
  description = "Regions of the mirror's members, one bucket each, in order. The first is this instance's zone (blob.mirror.zone): reads prefer it, and its bucket signs download URLs."
  type        = list(string)

  validation {
    condition     = length(var.regions) >= 2 && length(distinct(var.regions)) == length(var.regions)
    error_message = "regions must name at least two different regions."
  }
}

variable "app_role_arn" {
  description = "ARN of the IAM role artifactweave runs as (its instance profile's or workload's role). Every bucket policy grants it that bucket's objects."
  type        = string

  validation {
    condition     = can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:role/", var.app_role_arn))
    error_message = "app_role_arn must be an IAM role ARN."
  }
}

variable "store_prefix" {
  description = "Key prefix artifactweave writes under in each bucket (blob.stores.<name>.s3.prefix). Empty writes at the bucket's root."
  type        = string
  default     = ""
}

variable "expiring_prefixes" {
  description = "Key prefixes, from each bucket's root, whose objects expire after the given number of days, for example { \"scratch/\" = 1 }."
  type        = map(number)
  default     = {}
}

variable "force_destroy" {
  description = "Let terraform destroy delete the buckets with every object still in them. Off, a destroy fails while a bucket holds anything."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags for every bucket."
  type        = map(string)
  default     = {}
}

variable "write_quorum" {
  description = "Copies stored before an upload succeeds (blob.mirror.write_quorum). 1 keeps uploads going with a region down; the other copies follow in the background or through repair."
  type        = number
  default     = 1

  validation {
    condition     = var.write_quorum >= 1 && var.write_quorum <= length(var.regions)
    error_message = "write_quorum must be at least 1 and at most the number of regions."
  }
}
