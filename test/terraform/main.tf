# Terraform fixture applied against a running artifactweave in CI (decision 0015).
# P0 proves the provider's configure step; P2 adds the first resources. The URL and
# token come from variables so the same fixture runs locally and in the workflow.

terraform {
  required_providers {
    artifactory = {
      source  = "jfrog/artifactory"
      version = "12.11.14"
    }
  }
}

variable "url" {
  type        = string
  description = "Base URL of the artifactweave listener, without /artifactory."
}

variable "access_token" {
  type        = string
  description = "Admin access token. Any non-empty value passes configure in P0."
  sensitive   = true
}

provider "artifactory" {
  url          = var.url
  access_token = var.access_token
}

data "artifactory_repositories" "all" {}
