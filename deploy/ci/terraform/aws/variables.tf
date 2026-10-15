variable "region" {
  description = "Region of the state bucket and of every AWS scenario."
  type        = string
  default     = "eu-west-2"
}

variable "github_subject_prefix" {
  description = "The OIDC subject prefix of this repository. It uses GitHub's immutable subject, which names the owner and repository with their IDs; read it with `gh api repos/{owner}/{repo}/actions/oidc/customization/sub --jq .sub_claim_prefix`."
  type        = string
  default     = "repo:weaveplatform@336817058/artifactweave@1415179278"
}

variable "conformance_environment" {
  description = "GitHub environment whose jobs assume the conformance role (cloud-conformance.yml)."
  type        = string
  default     = "cloud-conformance"
}

variable "platform_environment" {
  description = "GitHub environment whose jobs assume the deployer role (platform-auth-check.yml, and the scenario workflow in #129)."
  type        = string
  default     = "cloud-platform"
}

variable "dns_zone_id" {
  description = "The Route 53 hosted zone for aws.artifact.weaveplatform.run, created by CLI and delegated at GoDaddy; imported here, never recreated."
  type        = string
  default     = "Z06607493A97HOZP218UN"
}

variable "scenario_bucket_prefix" {
  description = "Name prefix of every scenario bucket; the deployer administers buckets with this prefix only."
  type        = string
  default     = "artifactweave-"
}

variable "scenario_role_path" {
  description = "IAM path of every role a scenario creates (S3 replication, for one). The deployer may create and change roles under this path only, and only with the scenario permissions boundary attached."
  type        = string
  default     = "/artifactweave-scenarios/"
}

variable "budget_amount" {
  description = "Monthly cost budget for the account, in USD. Alerts at 50%, 90% and 100% of actual spend."
  type        = number
  default     = 20
}

variable "budget_alert_email" {
  description = "Address the budget alerts go to, needed only when the budget is created; the Makefile passes BUDGET_EMAIL, so no address is committed. Null on later runs, which leave the existing alerts alone."
  type        = string
  default     = null

  validation {
    condition     = var.budget_alert_email == null || can(regex("^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$", var.budget_alert_email))
    error_message = "budget_alert_email must be an email address."
  }
}
