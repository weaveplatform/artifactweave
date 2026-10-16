variable "project_id" {
  description = "GCP project that holds the Cloud Storage bucket. Nothing is created anywhere else on GCP."
  type        = string
}

variable "subscription_id" {
  description = "Azure subscription that holds the storage account. Nothing is created anywhere else on Azure."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$", var.subscription_id))
    error_message = "subscription_id must be a subscription ID (a GUID)."
  }
}

variable "name" {
  description = "Start of every resource name. The S3 and Cloud Storage buckets are <name>-<8 hex>, which must fit 63 characters; the storage account takes the name's first 16 letters and digits, lowercased, then the same 8 hex; the resource group this root creates is <name>-rg."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{0,53}$", var.name))
    error_message = "name must be 1 to 54 lowercase letters, digits and hyphens, starting with a letter or digit."
  }
}

variable "home_cloud" {
  description = "The cloud artifactweave runs in: aws, gcp or azure. Its member is the instance's zone (blob.mirror.zone), which reads prefer and whose store signs download URLs first."
  type        = string

  validation {
    condition     = contains(["aws", "gcp", "azure"], var.home_cloud)
    error_message = "home_cloud must be aws, gcp or azure."
  }
}

variable "aws_region" {
  description = "AWS region of the S3 member."
  type        = string
}

variable "gcp_location" {
  description = "GCP region of the Cloud Storage member, for example europe-west2. A single region: a dual-region bucket's own copies are invisible to the mirror."
  type        = string
}

variable "azure_location" {
  description = "Azure region of the storage account member, for example uksouth. The resource group this root creates is there too."
  type        = string
}

variable "app_role_arn" {
  description = "ARN of the IAM role artifactweave uses on AWS. The bucket policy grants it the bucket's objects."
  type        = string

  validation {
    condition     = can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:role/", var.app_role_arn))
    error_message = "app_role_arn must be an IAM role ARN."
  }
}

variable "app_service_account" {
  description = "Email of the service account artifactweave uses on GCP. It gets object rights on the bucket, and signs download URLs (blob.stores.<name>.gcs.google_access_id)."
  type        = string

  validation {
    condition     = can(regex("^[^@]+@[^@]+\\.gserviceaccount\\.com$", var.app_service_account))
    error_message = "app_service_account must be a service account email."
  }
}

variable "app_principal_id" {
  description = "Object ID of the identity artifactweave uses on Azure (a managed identity, or a service principal). It gets Storage Blob Data Contributor on the account."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$", var.app_principal_id))
    error_message = "app_principal_id must be an object ID (a GUID)."
  }
}

variable "additional_principals" {
  description = "Further principals that get Storage Blob Data Contributor on the storage account, by a static name: an operator who runs artifactweave or mints user delegation SAS from a workstation, for one. principal_type (User, Group or ServicePrincipal) is optional."
  type = map(object({
    principal_id   = string
    principal_type = optional(string)
  }))
  default = {}

  validation {
    condition     = !contains(keys(var.additional_principals), "app")
    error_message = "additional_principals cannot use the name app, which is app_principal_id's."
  }
}

variable "resource_group_name" {
  description = "An existing Azure resource group to create the storage account in. Null creates <name>-rg in azure_location."
  type        = string
  default     = null
}

variable "container" {
  description = "Blob container in the storage account (blob.stores.<name>.azure.container)."
  type        = string
  default     = "artifactweave"
}

variable "store_prefix" {
  description = "Key prefix artifactweave writes under in every member (blob.stores.<name>.<backend>.prefix). Empty writes at each bucket's or container's root."
  type        = string
  default     = ""
}

variable "expiring_prefixes" {
  description = "Key prefixes, from each member's root, whose objects expire after the given number of days, for example { \"scratch/\" = 1 }."
  type        = map(number)
  default     = {}
}

variable "force_destroy" {
  description = "Let terraform destroy delete the S3 and Cloud Storage buckets with every object still in them. Off, a destroy fails while a bucket holds anything. Deleting a storage account deletes its blobs regardless."
  type        = bool
  default     = false
}

variable "labels" {
  description = "Labels for every resource that takes them: tags on AWS and Azure, labels on GCP. Cloud Storage's rules apply to all of them: keys start with a lowercase letter, and keys and values hold lowercase letters, digits, underscores and hyphens, 63 characters at most."
  type        = map(string)
  default     = {}

  validation {
    condition = alltrue([
      for k, v in var.labels : can(regex("^[a-z][a-z0-9_-]{0,62}$", k)) && can(regex("^[a-z0-9_-]{0,63}$", v))
    ])
    error_message = "labels must follow Cloud Storage's label rules: lowercase letters, digits, underscores and hyphens, keys starting with a letter, 63 characters at most."
  }
}

variable "write_quorum" {
  description = "Copies stored before an upload succeeds (blob.mirror.write_quorum). 2 means every upload is on two providers before it is acknowledged, so losing any one provider loses nothing, and uploads still go on while one is down; 1 keeps them going with two down."
  type        = number
  default     = 2

  validation {
    condition     = var.write_quorum >= 1 && var.write_quorum <= 3
    error_message = "write_quorum must be 1, 2 or 3: there are three members."
  }
}
