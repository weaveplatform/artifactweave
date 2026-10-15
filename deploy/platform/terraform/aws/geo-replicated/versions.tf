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

# Credentials are your own AWS CLI login (`aws login`). The Makefile refuses
# to plan or apply unless that login's account holds the delegated zone.
provider "aws" {
  region = var.region
  default_tags {
    tags = {
      managed-by = "artifactweave"
    }
  }
}

provider "aws" {
  alias  = "replica"
  region = var.replica_region
  default_tags {
    tags = {
      managed-by = "artifactweave"
    }
  }
}
