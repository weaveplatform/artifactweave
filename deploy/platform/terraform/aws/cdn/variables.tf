variable "name" {
  description = "Start of every name: the bucket and the CloudFront origin access control, cache policy and key group are <name>-<8 hex>, within S3's 63 characters."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{0,53}$", var.name))
    error_message = "name must be 1 to 54 lowercase letters, digits and hyphens, starting with a letter or digit, so <name>-<8 hex> fits S3's 63 characters."
  }
}

variable "region" {
  description = "Region of the bucket, where artifactweave writes (blob.s3.region). CloudFront itself is global."
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

variable "cloudfront_public_key" {
  description = "The PEM public key (RSA 2048 or ECDSA P-256) of the key pair artifactweave signs CloudFront URLs with. Generate the pair yourself and keep the private half out of Terraform: the state holds this value."
  type        = string
}

variable "cloudfront_private_key_file" {
  description = "Where the private half of the key pair is on the host artifactweave runs on (blob.s3.cdn.cloudfront.private_key_file). Terraform never reads it; the configuration names it."
  type        = string
  default     = "/etc/artifactweave/cloudfront-private-key.pem"
}

variable "domain_name" {
  description = "A host of your own for the CDN, such as cdn.example.com, a sibling of artifactweave's host and never a subdomain of it. Null serves from the distribution's *.cloudfront.net host."
  type        = string
  default     = null
}

variable "zone_id" {
  description = "ID of the Route 53 public hosted zone holding domain_name, for its certificate's validation record and alias records. Set it with domain_name, and only then."
  type        = string
  default     = null
}

variable "price_class" {
  description = "Which CloudFront edge locations serve the blobs: PriceClass_All, PriceClass_200 or PriceClass_100."
  type        = string
  default     = "PriceClass_All"
}

variable "store_prefix" {
  description = "Key prefix artifactweave writes under (blob.s3.prefix). CloudFront may read only the committed blobs under it."
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
  description = "Tags for the bucket, the distribution and the certificate."
  type        = map(string)
  default     = {}
}
