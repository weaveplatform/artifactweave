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
# (`gcloud auth application-default login`). The organisation enforces
# iam.disableServiceAccountKeyCreation, so nothing here uses a service
# account key.
provider "google" {
  project = var.project_id
  region  = var.region
}
