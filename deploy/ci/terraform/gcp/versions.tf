terraform {
  # import blocks with expressions in id need 1.6; mocked tests need 1.7.
  required_version = ">= 1.7"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.6"
    }
  }
}

# Credentials are your own Application Default Credentials
# (`gcloud auth application-default login`).
provider "google" {
  project = var.project_id
  region  = var.region
}

# The budget API bills its calls to a project, so the budget's requests name
# this one (user_project_override) rather than the ADC's quota project. Only
# the budget uses it: the same header makes the OpenID userinfo call that
# finds the operator's email come back empty.
provider "google" {
  alias                 = "billing"
  project               = var.project_id
  region                = var.region
  user_project_override = true
  billing_project       = var.project_id
}
