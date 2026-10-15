terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.67"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.9"
    }
  }
}

# Credentials are your own AWS CLI login (`aws login`), or the deployer's in a
# scenario run. The Makefile refuses to plan or apply unless that login's
# account holds the delegated zone. The provider's region is the primary;
# each bucket in modules/aws/storage names its own.
provider "aws" {
  region = var.primary_region
  default_tags {
    tags = {
      managed-by = "artifactweave"
    }
  }
}
