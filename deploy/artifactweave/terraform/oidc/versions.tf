terraform {
  required_version = ">= 1.6"
  required_providers {
    platform = {
      source = "jfrog/platform"
      # Keep in step with the pins and checksums in test/terraform/run.sh
      # (decision 0030, rule A3).
      version = "2.2.11"
    }
  }
}

provider "platform" {
  url          = var.url
  access_token = var.access_token
}
