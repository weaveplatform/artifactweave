terraform {
  required_version = ">= 1.6"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.6"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.9"
    }
  }
}

# Credentials are your own Application Default Credentials
# (`gcloud auth application-default login`), or the deployer's in a scenario
# run. Each bucket names its own location, so the provider's region is only a
# default.
provider "google" {
  project = var.project_id
  region  = var.primary_region
}
