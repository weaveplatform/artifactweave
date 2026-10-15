terraform {
  required_version = ">= 1.9"
  required_providers {
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

# Credentials are the Azure CLI login (`az login`) or a CI identity. The
# subscription is an input rather than the CLI's active one, so a login that
# points elsewhere cannot create anything there.
provider "azurerm" {
  features {}
  subscription_id = var.subscription_id
  # The accounts allow no Shared Key, so the provider reaches their data
  # plane with Entra ID too.
  storage_use_azuread = true
  # Registering resource providers needs subscription-wide rights a
  # deployer scoped to one resource group lacks; Microsoft.Storage is
  # registered once, ahead of this root.
  resource_provider_registrations = "none"
}
