terraform {
  # import blocks with expressions in id need 1.6; mocked tests need 1.7.
  required_version = ">= 1.7"
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 5.9"
    }
  }
}

# Credentials are your Azure CLI login (`az login`). The subscription is
# pinned here, not taken from the CLI's active one, so a login that points at
# another subscription cannot create anything there; the Makefile refuses to
# plan or apply unless the active subscription and tenant match as well.
provider "azurerm" {
  features {}
  subscription_id = var.subscription_id
  # The state account allows no Shared Key, so the provider reaches the data
  # plane with Entra ID too.
  storage_use_azuread = true
  # The provider registers these when it starts, plan included, so the list
  # holds only namespaces already registered; new ones are
  # azurerm_resource_provider_registration resources, registered on apply.
  resource_provider_registrations = "none"
  resource_providers_to_register  = ["Microsoft.ManagedIdentity", "Microsoft.Network", "Microsoft.Storage"]
}
