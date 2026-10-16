terraform {
  required_version = ">= 1.9"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.67"
    }
    google = {
      source  = "hashicorp/google"
      version = "~> 8.6"
    }
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 5.9"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.9"
    }
  }
}

# One provider per cloud, each with its own credentials: the AWS SDK chain
# (an `aws login` session, a CI role), Application Default Credentials
# (`gcloud auth application-default login`, a CI service account) and the
# Azure CLI login or a CI identity. The project and subscription are inputs
# rather than whatever each CLI points at, so a login aimed elsewhere cannot
# create anything there; AWS takes the account from its credentials.
provider "aws" {
  region = var.aws_region
}

provider "google" {
  project = var.project_id
}

provider "azurerm" {
  features {}
  subscription_id = var.subscription_id
  # The account allows no Shared Key, so the provider reaches its data plane
  # with Entra ID too.
  storage_use_azuread = true
  # Registering resource providers needs subscription-wide rights a deployer
  # scoped to one resource group lacks; Microsoft.Storage is registered once,
  # ahead of this root.
  resource_provider_registrations = "none"
}
