terraform {
  required_version = ">= 1.17"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.6"
    }
  }
}
