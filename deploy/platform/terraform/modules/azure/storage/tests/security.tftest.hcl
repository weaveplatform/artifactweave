# The storage account's security baseline (decision 0030), checked on a plan
# against a mocked provider: no credentials and no calls to Azure. Scenarios
# that call this module check how they compose it, not these again. One
# assertion per run, so a failure names the invariant it breaks.

mock_provider "azurerm" {}

variables {
  account_name        = "example0a1b2cuks"
  resource_group_name = "example-rg"
  location            = "uksouth"
  principals = {
    app      = { principal_id = "00000000-0000-0000-0000-00000000000a" }
    operator = { principal_id = "00000000-0000-0000-0000-00000000000b", principal_type = "User" }
  }
}

run "no_blob_can_be_public" {
  command = plan

  assert {
    condition     = azurerm_storage_account.blobs.allow_nested_items_to_be_public == false && azurerm_storage_container.blobs.container_access_type == "private"
    error_message = "the container must be private, and the account must refuse to make any container public"
  }
}

run "plain_http_and_old_tls_are_refused" {
  command = plan

  assert {
    condition     = azurerm_storage_account.blobs.https_traffic_only_enabled && azurerm_storage_account.blobs.min_tls_version == "TLS1_2"
    error_message = "the account must refuse plain HTTP and TLS older than 1.2"
  }
}

run "access_is_entra_id_only" {
  command = plan

  assert {
    condition     = azurerm_storage_account.blobs.shared_access_key_enabled == false && azurerm_storage_account.blobs.default_to_oauth_authentication
    error_message = "Shared Key must stay disabled, so no account key exists to leak and every request carries an Entra ID identity"
  }
}

run "data_is_encrypted_at_rest" {
  command = plan

  assert {
    condition     = azurerm_storage_account.blobs.account_kind == "StorageV2" && length(azurerm_storage_account.blobs.customer_managed_key) == 0
    error_message = "the account must be a StorageV2 account on Azure's service-side encryption with Microsoft-managed keys"
  }
}

run "principals_get_blob_data_rights_on_this_account_only" {
  command = plan

  assert {
    # A mocked plan fills an unset principal_type with a generated value,
    # so only the operator's, which is set, is compared.
    condition = {
      for k, a in azurerm_role_assignment.app : k => [a.role_definition_name, a.principal_id, k == "operator" ? a.principal_type : ""]
      } == {
      app      = ["Storage Blob Data Contributor", "00000000-0000-0000-0000-00000000000a", ""]
      operator = ["Storage Blob Data Contributor", "00000000-0000-0000-0000-00000000000b", "User"]
    }
    error_message = "each principal must get blob data rights, as the type given, and no account administration"
  }
}

run "tags_reach_the_account" {
  command = plan

  variables {
    tags = { artifactweave-scenario = "multi-region", instance = "r1", expires-at = "1767225600" }
  }

  assert {
    condition     = azurerm_storage_account.blobs.tags == tomap({ artifactweave-scenario = "multi-region", instance = "r1", expires-at = "1767225600" })
    error_message = "the account, the one resource here that takes tags, must carry the tags given"
  }
}
