# The acceptance storage account's security invariants, checked on a plan
# against mocked providers: no credentials and no calls to Azure
# (decision 0030).

mock_provider "azurerm" {}
mock_provider "random" {}

run "account_takes_entra_id_only" {
  command = plan

  assert {
    condition     = azurerm_storage_account.blobs.shared_access_key_enabled == false
    error_message = "Shared Key must stay disabled, so no account key exists to leak"
  }

  assert {
    condition     = azurerm_storage_account.blobs.allow_nested_items_to_be_public == false
    error_message = "no container in the account may be made public"
  }

  assert {
    condition     = azurerm_storage_account.blobs.min_tls_version == "TLS1_2"
    error_message = "the account must refuse TLS older than 1.2"
  }

  assert {
    condition     = azurerm_storage_account.blobs.https_traffic_only_enabled
    error_message = "the account must refuse plain HTTP"
  }

  assert {
    condition     = azurerm_storage_container.blobs.container_access_type == "private"
    error_message = "the container must be private"
  }
}

run "abandoned_uploads_expire" {
  command = plan

  assert {
    condition = anytrue([
      for rule in azurerm_storage_management_policy.cleanup.rule :
      rule.enabled && contains(rule.filters[0].prefix_match, "${var.container}/${var.store_prefix}_uploads/")
    ])
    error_message = "a management policy rule must delete the temporary blobs an interrupted run leaves under _uploads/"
  }

  assert {
    condition = anytrue([
      for rule in azurerm_storage_management_policy.cleanup.rule :
      rule.enabled && contains(rule.filters[0].prefix_match, "${var.container}/conformance/")
    ])
    error_message = "a management policy rule must delete what an interrupted conformance run leaves under conformance/"
  }
}
