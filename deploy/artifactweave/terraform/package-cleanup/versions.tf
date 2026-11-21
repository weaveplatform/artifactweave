terraform {
  required_version = ">= 1.17"
  required_providers {
    artifactory = {
      source  = "jfrog/artifactory"
      version = "12.11.14"
    }
  }
}

provider "artifactory" {
  url          = var.url
  access_token = var.access_token
}
