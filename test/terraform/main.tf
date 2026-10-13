# Terraform fixture applied against a running artifactweave (decision 0015).
# run.sh installs the provider from a filesystem mirror built from the pinned
# release zip, so no run depends on registry.terraform.io. The URL and token
# come from variables so the same fixture runs locally and in CI.

terraform {
  required_providers {
    artifactory = {
      source = "jfrog/artifactory"
      # Keep in step with PROVIDER_VERSION and its checksums in run.sh.
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
  description = "Admin access token; the provider sends it as a Bearer token."
  sensitive   = true
}

variable "with_properties" {
  type        = bool
  default     = true
  description = "False on the first apply, before run.sh deploys the file the properties are set on."
}

provider "artifactory" {
  url          = var.url
  access_token = var.access_token
}

resource "artifactory_local_generic_repository" "fixture" {
  key         = "tf-generic-local"
  description = "applied by the terraform job"
}

# The provider cannot deploy content, so run.sh deploys fixture.txt between a
# first apply with with_properties = false and the full one.
resource "artifactory_item_properties" "fixture" {
  count     = var.with_properties ? 1 : 0
  repo_key  = artifactory_local_generic_repository.fixture.key
  item_path = "fixture.txt"
  properties = {
    "team" = ["platform"]
    "tier" = ["gold", "silver"]
  }
}
