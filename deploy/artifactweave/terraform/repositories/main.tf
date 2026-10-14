# Repositories reference: a local generic repository and properties on a file
# in it, managed with the jfrog/artifactory provider. test/terraform/run.sh
# applies it in CI, requires an empty plan, then destroys it (decisions 0015
# and 0030). The URL and token come from variables, so the same configuration
# runs against any artifactweave.

terraform {
  required_version = ">= 1.6"
  required_providers {
    artifactory = {
      source = "jfrog/artifactory"
      # Keep in step with the pins and checksums in test/terraform/run.sh.
      version = "12.11.14"
    }
  }
}

variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory."
}

variable "access_token" {
  type        = string
  description = "Admin access token, sent as a Bearer token."
  sensitive   = true
}

variable "with_properties" {
  type        = bool
  default     = true
  description = "Whether to set the properties on fixture.txt. The provider cannot deploy files, so apply with false, deploy fixture.txt, then apply with true."
}

provider "artifactory" {
  url          = var.url
  access_token = var.access_token
}

resource "artifactory_local_generic_repository" "fixture" {
  key         = "tf-generic-local"
  description = "managed by Terraform"
}

# The provider cannot deploy content, so the file the properties go on is
# deployed between a first apply with with_properties = false and the full one.
resource "artifactory_item_properties" "fixture" {
  count     = var.with_properties ? 1 : 0
  repo_key  = artifactory_local_generic_repository.fixture.key
  item_path = "fixture.txt"
  properties = {
    "team" = ["platform"]
    "tier" = ["gold", "silver"]
  }
}
