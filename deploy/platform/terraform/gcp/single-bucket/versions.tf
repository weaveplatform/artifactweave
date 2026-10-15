terraform {
  required_version = ">= 1.9"
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

# Credentials are Application Default Credentials (`gcloud auth
# application-default login`, a CI service account). The bucket names its own
# location, so the provider needs only the project.
provider "google" {
  project = var.project_id
}
