variable "project_id" {
  description = "Project that holds the bucket, the load balancer and the certificate. Nothing is created anywhere else."
  type        = string
}

variable "name" {
  description = "Start of every name: the bucket, the load balancer's pieces and the certificate's are <name>-<8 hex>, within the 63 characters Cloud Storage and Compute Engine allow."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,53}$", var.name))
    error_message = "name must be 1 to 54 lowercase letters, digits and hyphens, starting with a letter, so <name>-<8 hex> fits 63 characters."
  }
}

variable "location" {
  description = "Region of the bucket, where artifactweave writes, for example europe-west2. The load balancer and Cloud CDN are global."
  type        = string
}

variable "app_service_account" {
  description = "Email of the service account artifactweave runs as. It gets object rights on the bucket, and signs the storage URLs X-JFrog-Download-Redirect-To: S3 asks for (blob.gcs.google_access_id)."
  type        = string

  validation {
    condition     = can(regex("^[^@]+@[^@]+\\.gserviceaccount\\.com$", var.app_service_account))
    error_message = "app_service_account must be a service account email."
  }
}

variable "domain_name" {
  description = "The CDN's host, such as cdn.example.com, a sibling of artifactweave's host and never a subdomain of it, served with a Google-managed certificate."
  type        = string
}

variable "dns_zone" {
  description = "Name of the Cloud DNS managed zone in project_id that holds domain_name, where the certificate's authorisation record and the host's A record go. Null creates no records: add the two in the dns_records output where the domain is served."
  type        = string
  default     = null
}

variable "signed_url_key_name" {
  description = "Name of the signed URL key artifactweave signs with (blob.gcs.cdn.cloud_cdn.key_name), as given to gcloud compute backend-buckets add-signed-url-key."
  type        = string
  default     = "artifactweave"

  validation {
    condition     = can(regex("^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$", var.signed_url_key_name))
    error_message = "signed_url_key_name must be 1 to 63 lowercase letters, digits and hyphens, starting with a letter."
  }
}

variable "signed_url_key_added" {
  description = "Whether the signed URL key has been added to the backend bucket. Set it once you have added the key: it lets Cloud CDN's fill account read the blobs, an account Google creates with the project's first key. Until then Cloud CDN serves nothing."
  type        = bool
  default     = false
}

variable "cloud_cdn_key_file" {
  description = "Where the signed URL key is on the host artifactweave runs on (blob.gcs.cdn.cloud_cdn.key_file): 16 random bytes, base64url. Terraform never reads it; the configuration names it."
  type        = string
  default     = "/etc/artifactweave/cloud-cdn-key"
}

variable "store_prefix" {
  description = "Object prefix artifactweave writes under (blob.gcs.prefix). Cloud CDN may read only the committed blobs under it."
  type        = string
  default     = ""
}

variable "expiring_prefixes" {
  description = "Object prefixes, from the bucket's root, whose objects expire after the given number of days, for example { \"scratch/\" = 1 }."
  type        = map(number)
  default     = {}
}

variable "force_destroy" {
  description = "Let terraform destroy delete the bucket with every object still in it. Off, a destroy fails while the bucket holds anything."
  type        = bool
  default     = false
}

variable "labels" {
  description = "Labels for the bucket, the load balancer's address and forwarding rule, and the certificate's pieces."
  type        = map(string)
  default     = {}
}
