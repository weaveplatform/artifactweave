terraform {
  required_version = ">= 1.11"
  required_providers {
    artifactory = {
      source = "jfrog/artifactory"
      # Keep in step with the pins and checksums in test/terraform/run.sh
      # (decision 0030, rule A3).
      version = "12.11.14"
    }
  }
}

provider "artifactory" {
  url          = var.url
  access_token = var.access_token
}
