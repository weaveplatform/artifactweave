variable "name" {
  description = "Name of the origin access control, the cache policy and the key group, and the start of each public key's: 1 to 64 letters, digits, hyphens and underscores, unique among the account's CloudFront resources of each kind."
  type        = string

  # An origin access control's name is the shortest CloudFront allows, at
  # 64 characters.
  validation {
    condition     = can(regex("^[A-Za-z0-9_-]{1,64}$", var.name))
    error_message = "name must be 1 to 64 letters, digits, hyphens and underscores."
  }
}

variable "origin_bucket" {
  description = "The bucket CloudFront reads from, from modules/aws/storage's outputs of the same names. Pass them one by one rather than the module itself, whose bucket policy waits for this module's statement. The origin is the bucket's root, so artifactweave's blob.s3.cdn.cloudfront.path_prefix stays empty."
  type = object({
    bucket = string
    arn    = string
    region = string
  })
}

variable "store_prefix" {
  description = "Key prefix artifactweave writes under (blob.s3.prefix), as given to modules/aws/storage. CloudFront may read only the committed blobs under it, <store_prefix>sha256/."
  type        = string
  default     = ""
}

variable "public_keys" {
  description = "PEM public keys (RSA 2048 or ECDSA P-256) whose private halves sign artifactweave's URLs, by a static name. Every one is trusted: to rotate, add the new key, point artifactweave at it, then remove the old one once the URLs it signed have expired. Terraform state holds these values, so never pass a private key."
  type        = map(string)

  # CloudFront allows five public keys in a key group.
  validation {
    condition     = length(var.public_keys) >= 1 && length(var.public_keys) <= 5
    error_message = "public_keys must hold one to five keys."
  }

  validation {
    condition     = alltrue([for k in keys(var.public_keys) : can(regex("^[a-z0-9-]{1,32}$", k))])
    error_message = "each public key's name must be 1 to 32 lowercase letters, digits and hyphens."
  }

  validation {
    condition     = alltrue([for v in values(var.public_keys) : startswith(trimspace(v), "-----BEGIN PUBLIC KEY-----")])
    error_message = "each public key must be a PEM public key (BEGIN PUBLIC KEY), never a private key."
  }
}

variable "domain_name" {
  description = "A host of your own for the distribution, such as cdn.example.com, served with an ACM certificate in us-east-1 validated through zone_id. Make it a sibling of artifactweave's host, never a subdomain of it, so clients drop their credentials on the redirect; artifactweave refuses to start otherwise. Null serves from the distribution's *.cloudfront.net host."
  type        = string
  default     = null

  validation {
    condition     = var.domain_name == null || can(regex("^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$", var.domain_name))
    error_message = "domain_name must be a lowercase host name, such as cdn.example.com."
  }
}

variable "zone_id" {
  description = "ID of the Route 53 public hosted zone that holds domain_name: the certificate's validation record and the host's A and AAAA alias records go there. Set it with domain_name, and only then."
  type        = string
  default     = null

  validation {
    condition     = (var.zone_id == null) == (var.domain_name == null)
    error_message = "zone_id and domain_name go together: set both for a host of your own, or neither."
  }
}

variable "price_class" {
  description = "Which edge locations serve the blobs: PriceClass_All, PriceClass_200, or PriceClass_100 (North America, Europe and Israel). Viewers elsewhere are served from the nearest included location."
  type        = string
  default     = "PriceClass_All"

  validation {
    condition     = contains(["PriceClass_All", "PriceClass_200", "PriceClass_100"], var.price_class)
    error_message = "price_class must be PriceClass_All, PriceClass_200 or PriceClass_100."
  }
}

variable "tags" {
  description = "Tags for the distribution and the certificate, the resources here that take them."
  type        = map(string)
  default     = {}
}
