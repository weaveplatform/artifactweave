variable "subscription_id" {
  description = "The lab subscription every Azure scenario runs in. Nothing is created anywhere else."
  type        = string
  default     = "c33dbc35-f86d-4b7c-a81e-2a33eb3f7946"
}

variable "location" {
  description = "Region of the resource groups, identities and state account."
  type        = string
  default     = "uksouth"
}

variable "resource_group" {
  description = "The bootstrap's resource group, created by CLI with the DNS zone; imported here."
  type        = string
  default     = "artifactweave-bootstrap-rg"
}

variable "scenarios_resource_group" {
  description = "The resource group every scenario instance is created in. The deployer's rights stop at its boundary."
  type        = string
  default     = "artifactweave-scenarios-rg"
}

variable "github_subject_prefix" {
  description = "The OIDC subject prefix of this repository. It uses GitHub's immutable subject, which names the owner and repository with their IDs; read it with `gh api repos/{owner}/{repo}/actions/oidc/customization/sub --jq .sub_claim_prefix`."
  type        = string
  default     = "repo:weaveplatform@336817058/artifactweave@1415179278"
}

variable "conformance_environment" {
  description = "GitHub environment whose jobs act as the data-plane identity (cloud-conformance.yml)."
  type        = string
  default     = "cloud-conformance"
}

variable "platform_environment" {
  description = "GitHub environment whose jobs act as the deployer identity (platform-auth-check.yml, and the scenario workflow in #129)."
  type        = string
  default     = "cloud-platform"
}

variable "budget_amount" {
  description = "Monthly budget for the subscription, in its billing currency. Alerts go to the subscription's owners at 50%, 90% and 100%."
  type        = number
  default     = 20
}

variable "budget_start" {
  description = "First month the budget covers, as the first day of a month in RFC 3339."
  type        = string
  default     = "2026-10-01T00:00:00Z"
}
