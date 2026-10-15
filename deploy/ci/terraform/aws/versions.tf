terraform {
  # The S3 backend's use_lockfile needs 1.10; mocked tests need 1.7.
  required_version = ">= 1.10"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.67"
    }
  }
}

# Credentials are your own AWS CLI login (`aws login`). The Makefile refuses
# to plan or apply unless that login's account holds the delegated zone.
provider "aws" {
  region = var.region
  default_tags {
    tags = {
      managed-by = "artifactweave-bootstrap"
      issue      = "128"
    }
  }
}
