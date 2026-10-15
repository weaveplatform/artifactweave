# How the scenario composes modules/azure/storage with Azure's geo
# replication, checked on a plan against mocked providers (decision 0030,
# S10). The account's own baseline is the module's tests.

mock_provider "azurerm" {}
mock_provider "random" {}

# The account name, and so the rendered configuration, comes from the random
# suffix, which a mocked provider would leave unknown at plan.
override_resource {
  target          = random_string.suffix
  override_during = plan
  values = {
    result = "0a1b2c"
  }
}

variables {
  subscription_id  = "00000000-0000-0000-0000-000000000000"
  name             = "artifactweave-example"
  location         = "uksouth"
  app_principal_id = "00000000-0000-0000-0000-00000000000a"
}

# The README's example inputs ("Inputs to set") must render the README's
# example configuration, so the documentation cannot drift from the code.
run "readme_example" {
  command = plan

  variables {
    subscription_id  = "00000000-0000-0000-0000-000000000000"
    name             = "artifactweave-example"
    location         = "uksouth"
    app_principal_id = "00000000-0000-0000-0000-00000000000a"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config) == yamldecode(one(regex("(?s)<!-- BEGIN_CONFIG_EXAMPLE -->\\n```yaml\\n(.*?)```\\n<!-- END_CONFIG_EXAMPLE -->", file("README.md"))))
    error_message = "the README's configuration example must be what its example inputs render"
  }
}

# A proof's instance as platform-scenarios.yml names it, longer than any it
# produces today (pr<number>-<run id>-<initials>-<step>), with
# scenario-ci.sh's NAME_PREFIX, the tags it sets, and the scenarios resource
# group the deployer's rights end at.
run "ci_run" {
  command = plan

  variables {
    name                = "awscn-pr1234-123456789012-gr-t"
    resource_group_name = "scenarios-rg"
    tags = {
      artifactweave-scenario = "geo-replicated"
      instance               = "pr1234-123456789012-gr-t"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition     = output.account == "awscnpr123412340a1b2cuks" && can(regex("^[a-z0-9]{3,24}$", output.account))
    error_message = "the account must be the name's letters and digits, cut, then the suffix and the location's first three letters, within 24"
  }

  assert {
    condition     = length(azurerm_resource_group.this) == 0 && output.resource_group == "scenarios-rg"
    error_message = "given a resource group, the root must create none and put the account in it"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "expires_at must carry the tag reap reads from the state"
  }

  assert {
    condition     = output.storage_host == "awscnpr123412340a1b2cuks.blob.core.windows.net"
    error_message = "storage_host must be the primary blob endpoint, where artifactweave's redirects point"
  }

  assert {
    condition     = output.secondary_storage_host == "awscnpr123412340a1b2cuks-secondary.blob.core.windows.net"
    error_message = "secondary_storage_host must be the paired region's read endpoint, which test/platform reads the blob from"
  }
}

run "geo_redundant_with_a_readable_secondary_by_default" {
  command = plan

  assert {
    condition     = module.storage.secondary_host != null
    error_message = "the default must keep a readable copy in the paired region (RA-GZRS)"
  }
}

run "an_unreadable_copy_has_no_secondary_host" {
  command = plan

  variables {
    replication_type = "GZRS"
  }

  assert {
    condition     = output.secondary_storage_host == null
    error_message = "a GZRS copy is readable only after a failover, so secondary_storage_host must be null"
  }
}

run "local_redundancy_is_refused" {
  command = plan

  variables {
    replication_type = "LRS"
  }

  expect_failures = [var.replication_type]
}

run "without_a_resource_group_one_is_created" {
  command = plan

  variables {
    tags = { team = "platform" }
  }

  assert {
    condition = (
      azurerm_resource_group.this[0].name == "artifactweave-example-rg" &&
      azurerm_resource_group.this[0].location == "uksouth" &&
      azurerm_resource_group.this[0].tags == tomap({ team = "platform" })
    )
    error_message = "with no resource_group_name the root must create <name>-rg in location, tagged"
  }
}

run "inputs_reach_the_store" {
  command = plan

  variables {
    container    = "blobs"
    store_prefix = "artifacts/"
  }

  assert {
    condition = (
      yamldecode(output.artifactweave_config).blob.azure.container == "blobs" &&
      yamldecode(output.artifactweave_config).blob.azure.prefix == "artifacts/"
    )
    error_message = "container and store_prefix must reach the store"
  }
}

run "additional_principals_cannot_replace_the_app" {
  command = plan

  variables {
    additional_principals = { app = { principal_id = "00000000-0000-0000-0000-00000000000b" } }
  }

  expect_failures = [var.additional_principals]
}

run "app_principal_id_is_checked" {
  command = plan

  variables {
    app_principal_id = ""
  }

  expect_failures = [var.app_principal_id]
}
