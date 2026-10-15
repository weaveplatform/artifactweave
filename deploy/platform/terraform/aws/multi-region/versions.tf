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
# role), and the account is theirs. Every bucket names its own region (the
# per-resource region of the AWS provider 6), so this one provider serves
# all the members; its own region is the first member's.
provider "aws" {
  region = var.regions[0]
}
