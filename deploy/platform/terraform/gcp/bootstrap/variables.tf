variable "project_id" {
  description = "The lab project every GCP scenario runs in."
  type        = string
  default     = "project-0c0b2d7a-9ad7-4221-9a8"
}

variable "region" {
  description = "Location of the state bucket."
  type        = string
  default     = "europe-west2"
}

variable "github_subject_prefix" {
  description = "The OIDC subject prefix of this repository. It uses GitHub's immutable subject, which names the owner and repository with their IDs; read it with `gh api repos/{owner}/{repo}/actions/oidc/customization/sub --jq .sub_claim_prefix`."
  type        = string
  default     = "repo:weaveplatform@336817058/artifactweave@1415179278"
}

variable "conformance_environment" {
  description = "GitHub environment whose jobs act as the data-plane service account (cloud-conformance.yml)."
  type        = string
  default     = "cloud-conformance"
}

variable "platform_environment" {
  description = "GitHub environment whose jobs act as the deployer service account (platform-auth-check.yml, and the scenario workflow in #129)."
  type        = string
  default     = "cloud-platform"
}

variable "dns_zone" {
  description = "The Cloud DNS managed zone for gcp.artifact.weaveplatform.run, created by CLI and delegated at GoDaddy; imported here, never recreated."
  type        = string
  default     = "gcp-artifact-weaveplatform-run"
}

variable "billing_account" {
  description = "Billing account the project bills to, for the budget."
  type        = string
  default     = "011F23-67D58D-596CD4"
}

variable "budget_amount" {
  description = "Monthly budget for the project, in the billing account's currency. Alerts go to the billing account's administrators at 50%, 90% and 100%."
  type        = number
  default     = 20
}
