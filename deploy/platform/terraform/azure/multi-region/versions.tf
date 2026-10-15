terraform {
  required_version = ">= 1.6"
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

# Credentials are your Azure CLI login (`az login`), or the deployer's in a
# scenario run. The subscription is pinned here, not taken from the CLI's
# active one, so a login that points at another subscription cannot create
# anything there; the Makefile refuses to plan or apply unless the active
# subscription and tenant match as well.
provider "azurerm" {
  features {}
  subscription_id = var.subscription_id
  # The accounts allow no Shared Key, so the provider reaches the data plane
  # with Entra ID too.
  storage_use_azuread             = true
  resource_provider_registrations = "none"
  resource_providers_to_register  = var.register_resource_providers ? ["Microsoft.Storage", "Microsoft.ManagedIdentity"] : []
}
