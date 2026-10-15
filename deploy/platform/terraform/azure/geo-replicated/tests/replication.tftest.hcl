# The geo-replicated storage account's invariants, checked on a plan against
# mocked providers: no credentials and no calls to Azure (decision 0030).

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
    condition     = azurerm_storage_account.blobs.min_tls_version == "TLS1_2" && azurerm_storage_account.blobs.https_traffic_only_enabled
    error_message = "the account must refuse plain HTTP and TLS older than 1.2"
  }

  assert {
    condition     = azurerm_storage_container.blobs.container_access_type == "private"
    error_message = "the container must be private"
  }
}

run "geo_redundant_with_a_readable_secondary" {
  command = plan

  assert {
    condition     = azurerm_storage_account.blobs.account_replication_type == "RAGZRS"
    error_message = "the default must be RA-GZRS: zone-redundant in the primary region, copied to the pair, readable there"
  }
}

run "local_redundancy_is_refused" {
  command = plan

  variables {
    replication_type = "LRS"
  }

  expect_failures = [var.replication_type]
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
}

# A scenario run applies as the deployer: it uses the bootstrap's scenarios
# resource group, grants no conformance identity, registers no providers and
# tags the account for reap.
run "scenario_run_stays_in_the_scenarios_group" {
  command = plan

  variables {
    resource_group_name         = "artifactweave-scenarios-rg"
    conformance_identity        = ""
    register_resource_providers = false
    scenario_labels = {
      artifactweave-scenario = "geo-replicated"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition     = length(azurerm_resource_group.this) == 0
    error_message = "a scenario run must not create a resource group; the deployer's rights end at the scenarios group"
  }

  assert {
    condition     = length(azurerm_role_assignment.conformance) == 0
    error_message = "a scenario run must not look up or grant the conformance identity"
  }

  assert {
    condition     = azurerm_storage_account.blobs.tags["expires-at"] == "1767225600" && output.expires_at == "1767225600"
    error_message = "the account and the expires_at output must carry the tag reap matches"
  }
}
