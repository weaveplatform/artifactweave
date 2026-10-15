# The storage account's security invariants, checked on a plan against a
# mocked provider: no credentials and no calls to Azure (decision 0030).
# Scenarios that call this module check how they compose it, not these again.

mock_provider "azurerm" {}

variables {
  account_name        = "awmr0a1b2c3duksouth"
  resource_group_name = "artifactweave-scenarios-rg"
  location            = "uksouth"
  container           = "artifactweave"
  store_prefix        = "local/"
  principals = {
    operator = "00000000-0000-0000-0000-000000000001"
  }
}

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

run "account_is_one_region" {
  command = plan

  assert {
    condition     = azurerm_storage_account.blobs.account_replication_type == "LRS"
    error_message = "the account keeps one region's copies: the mirror keeps the other, and the cloud's replicas never count toward its quorum (decision 0028)"
  }
}

run "abandoned_uploads_expire" {
  command = plan

  assert {
    condition = anytrue([
      for rule in azurerm_storage_management_policy.cleanup.rule :
      rule.enabled && contains(rule.filters[0].prefix_match, "artifactweave/local/_uploads/")
    ])
    error_message = "a management policy rule must delete the temporary blobs an interrupted run leaves under _uploads/"
  }
}

run "principals_get_blob_data_only" {
  command = plan

  assert {
    condition     = azurerm_role_assignment.app["operator"].role_definition_name == "Storage Blob Data Contributor"
    error_message = "each principal gets blob data rights on this account, not account administration"
  }

  assert {
    condition     = azurerm_role_assignment.app["operator"].principal_id == "00000000-0000-0000-0000-000000000001"
    error_message = "the role assignment must go to the principal given"
  }
}

run "account_name_is_checked" {
  command = plan

  variables {
    account_name = "Not-A-Valid-Name"
  }

  expect_failures = [var.account_name]
}

run "tags_reach_the_account" {
  command = plan

  variables {
    tags = { artifactweave-scenario = "multi-region", instance = "r1", expires-at = "1767225600" }
  }

  assert {
    condition     = azurerm_storage_account.blobs.tags["expires-at"] == "1767225600" && azurerm_storage_account.blobs.tags["instance"] == "r1"
    error_message = "the account must carry the scenario tags reap matches"
  }
}
