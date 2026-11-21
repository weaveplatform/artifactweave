terraform {
  required_version = ">= 1.17"
  required_providers {
    # Match the existing runner's release pins and verified checksums.
    artifactory = {
      source  = "jfrog/artifactory"
      version = "12.11.14"
    }
    platform = {
      source  = "jfrog/platform"
      version = "2.2.11"
    }
    project = {
      source  = "jfrog/project"
      version = "1.9.9"
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

provider "project" {
  url          = var.url
  access_token = var.access_token
}
