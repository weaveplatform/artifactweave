terraform {
  required_version = ">= 1.9"
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

# Credentials come from the AWS SDK chain (an `aws login` session, a CI
# role), and the account is theirs.
provider "aws" {
  region = var.region
}
