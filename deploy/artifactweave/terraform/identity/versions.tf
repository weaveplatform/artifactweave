terraform {
  required_version = ">= 1.6"
  required_providers {
    # Keep both in step with the pins and checksums in test/terraform/run.sh
    # (decision 0030, rule A3).
    artifactory = {
      source  = "jfrog/artifactory"
      version = "12.11.14"
    }
    platform = {
      source  = "jfrog/platform"
      version = "2.2.11"
    }
  }
}

provider "artifactory" {
  url          = var.url
  access_token = var.access_token
}

provider "platform" {
  url          = var.url
  access_token = var.access_token
}
