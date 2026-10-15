# How the scenario composes the storage-account module into a mirror,
# checked on a plan against mocked providers (decision 0030). The accounts'
# own security invariants are modules/azure/storage's tests.

mock_provider "azurerm" {
  mock_data "azurerm_client_config" {
    defaults = {
      object_id = "00000000-0000-0000-0000-00000000000a"
    }
  }
  mock_data "azurerm_user_assigned_identity" {
    defaults = {
      principal_id = "00000000-0000-0000-0000-00000000000c"
    }
  }
  mock_data "azurerm_resource_group" {
    defaults = {
      name     = "artifactweave-scenarios-rg"
      location = "uksouth"
    }
  }
}
mock_provider "random" {}

# Account names, and so the rendered configuration, come from the random
# suffix, which a mocked provider would leave unknown at plan.
override_resource {
  target          = random_string.suffix
  override_during = plan
  values = {
    result = "0a1b2c3d"
  }
}

run "two_regions_one_mirror" {
  command = plan

  assert {
    condition     = toset(keys(module.member)) == toset(["uksouth", "ukwest"])
    error_message = "there must be one member account in each of the two regions"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.backend == "mirror"
    error_message = "artifactweave must run the two accounts as a mirror"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.mirror.members == ["azure_uksouth", "azure_ukwest"]
    error_message = "the mirror's members must be the two stores, the primary region first"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.mirror.zone == "uksouth"
    error_message = "this instance's zone must be the primary region, so reads prefer it"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.mirror.write_quorum == 1
    error_message = "the default write quorum is one copy, so uploads continue with a region down"
  }

  assert {
    condition = alltrue([
      for name, store in yamldecode(output.artifactweave_config).blob.stores :
      store.backend == "azure" && store.zone == trimprefix(name, "azure_") && store.azure.credential == "default"
    ])
    error_message = "each store must be an azure store in its account's region, on the SDK credential chain"
  }

  assert {
    condition     = output.accounts == { uksouth = "awmr0a1b2c3duksouth", ukwest = "awmr0a1b2c3dukwest" }
    error_message = "each account must be named after the suffix and its region, within 24 characters"
  }

  assert {
    condition     = output.storage_host == "awmr0a1b2c3duksouth.blob.core.windows.net"
    error_message = "storage_host must be the primary account's blob endpoint"
  }

  assert {
    condition     = output.mirror_members[1].signed_url_prefix == "awmr0a1b2c3dukwest.blob.core.windows.net/artifactweave/"
    error_message = "mirror_members must list the secondary member second, with the prefix its signed URLs start with"
  }
}

run "operator_and_conformance_get_blob_rights" {
  command = plan

  assert {
    condition     = local.principals == { operator = "00000000-0000-0000-0000-00000000000a", conformance = "00000000-0000-0000-0000-00000000000c" }
    error_message = "whoever applies and the bootstrap's conformance identity must both get blob rights on the accounts"
  }

  assert {
    condition     = length(azurerm_resource_group.this) == 1
    error_message = "outside a scenario run the root creates its own resource group"
  }
}

# A scenario run applies as the deployer: it uses the bootstrap's scenarios
# resource group, grants no conformance identity (the deployer cannot read
# the bootstrap's group) and registers no providers.
run "scenario_run_stays_in_the_scenarios_group" {
  command = plan

  variables {
    resource_group_name         = "artifactweave-scenarios-rg"
    conformance_identity        = ""
    register_resource_providers = false
    scenario_labels = {
      artifactweave-scenario = "multi-region"
      instance               = "pr160-38712345678-mr-t"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition     = length(azurerm_resource_group.this) == 0
    error_message = "a scenario run must not create a resource group; the deployer's rights end at the scenarios group"
  }

  assert {
    condition     = length(data.azurerm_user_assigned_identity.conformance) == 0 && keys(local.principals) == ["operator"]
    error_message = "a scenario run must not look up or grant the conformance identity"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "the expires_at output must carry the tag reap reads from state"
  }
}

run "regions_must_differ" {
  command = plan

  variables {
    secondary_location = "uksouth"
  }

  expect_failures = [var.secondary_location]
}
