# State written before this root composed modules/azure/storage plans as
# moves rather than as destroy and create. The account's name now comes from
# name, so a plan of that state still replaces the account and what hangs
# off it; the resource group keeps its name and is unchanged.

# The storage account, inline before the module.
moved {
  from = azurerm_storage_account.blobs
  to   = module.storage.azurerm_storage_account.blobs
}

# Its container.
moved {
  from = azurerm_storage_container.blobs
  to   = module.storage.azurerm_storage_container.blobs
}

# Its management policy.
moved {
  from = azurerm_storage_management_policy.cleanup
  to   = module.storage.azurerm_storage_management_policy.cleanup
}

# The conformance identity's role assignment, now the module's app
# principal.
moved {
  from = azurerm_role_assignment.conformance[0]
  to   = module.storage.azurerm_role_assignment.app["app"]
}

# The assignment of whoever applied, now an operator in
# additional_principals.
moved {
  from = azurerm_role_assignment.operator
  to   = module.storage.azurerm_role_assignment.app["operator"]
}
