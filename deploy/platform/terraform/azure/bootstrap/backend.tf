# State lives in the account this root creates (state.tf). The first apply
# runs on local state through a git-ignored local_override.tf, which the
# Makefile writes while the account does not exist; `make migrate-state` then
# removes the override and moves the state into the account (decision 0030).
# The names are fixed, not generated, because a backend block cannot read a
# variable.
terraform {
  backend "azurerm" {
    subscription_id      = "c33dbc35-f86d-4b7c-a81e-2a33eb3f7946"
    resource_group_name  = "artifactweave-bootstrap-rg"
    storage_account_name = "awtfstatec33dbc35"
    container_name       = "tfstate"
    key                  = "bootstrap/azure.tfstate"
    # The account allows no Shared Key.
    use_azuread_auth = true
  }
}
