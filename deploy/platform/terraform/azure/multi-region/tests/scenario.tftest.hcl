# How the scenario composes modules/azure/storage into a mirror, checked on a
# plan against mocked providers (decision 0030, S10). The accounts' own
# baseline is the module's tests.

mock_provider "azurerm" {}
mock_provider "random" {}

# Account names, and so the rendered configuration, come from the random
# suffixes, which a mocked provider would leave unknown at plan.
override_resource {
  target          = random_string.suffix["uksouth"]
  override_during = plan
  values = {
    result = "0a1b2c"
  }
}

override_resource {
  target          = random_string.suffix["ukwest"]
  override_during = plan
  values = {
    result = "3d4e5f"
  }
}

variables {
  subscription_id  = "00000000-0000-0000-0000-000000000000"
  name             = "artifactweave-example"
  locations        = ["uksouth", "ukwest"]
  app_principal_id = "00000000-0000-0000-0000-00000000000a"
}

# The README's example inputs ("Inputs to set") must render the README's
# example configuration, so the documentation cannot drift from the code.
run "readme_example" {
  command = plan

  variables {
    subscription_id  = "00000000-0000-0000-0000-000000000000"
    name             = "artifactweave-example"
    locations        = ["uksouth", "ukwest"]
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
    name                = "awscn-pr1234-123456789012-mr-t"
    resource_group_name = "scenarios-rg"
    tags = {
      artifactweave-scenario = "multi-region"
      instance               = "pr1234-123456789012-mr-t"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition     = output.accounts == { uksouth = "awscnpr123412340a1b2cuks", ukwest = "awscnpr123412343d4e5fukw" }
    error_message = "each account must be the name's letters and digits, cut, then its suffix and the location's first three letters"
  }

  assert {
    condition     = alltrue([for a in values(output.accounts) : can(regex("^[a-z0-9]{3,24}$", a))])
    error_message = "a CI run's account names must fit Azure's 24 lowercase letters and digits"
  }

  assert {
    condition     = length(azurerm_resource_group.this) == 0 && output.resource_group == "scenarios-rg"
    error_message = "given a resource group, the root must create none and put the accounts in it"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "expires_at must carry the tag reap reads from the state"
  }

  assert {
    condition = output.mirror_members == [
      { store = "azure_uksouth", zone = "uksouth", signed_url_prefix = "awscnpr123412340a1b2cuks.blob.core.windows.net/artifactweave/" },
      { store = "azure_ukwest", zone = "ukwest", signed_url_prefix = "awscnpr123412343d4e5fukw.blob.core.windows.net/artifactweave/" },
    ]
    error_message = "mirror_members must list the members in order, with the prefix each one's signed URLs start with, which test/platform reads"
  }

  assert {
    condition     = output.storage_host == "awscnpr123412340a1b2cuks.blob.core.windows.net"
    error_message = "storage_host must be the first member's blob endpoint"
  }
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
    error_message = "with no resource_group_name the root must create <name>-rg in the first location, tagged"
  }
}

run "inputs_reach_every_store" {
  command = plan

  variables {
    container    = "blobs"
    store_prefix = "artifacts/"
    write_quorum = 2
  }

  assert {
    condition = (
      [for s in values(yamldecode(output.artifactweave_config).blob.stores) : [s.azure.container, s.azure.prefix]] == [["blobs", "artifacts/"], ["blobs", "artifacts/"]] &&
      yamldecode(output.artifactweave_config).blob.mirror.write_quorum == 2
    )
    error_message = "container and store_prefix must reach every store, and write_quorum the mirror"
  }
}

run "additional_principals_cannot_replace_the_app" {
  command = plan

  variables {
    additional_principals = { app = { principal_id = "00000000-0000-0000-0000-00000000000b" } }
  }

  expect_failures = [var.additional_principals]
}

run "locations_must_differ" {
  command = plan

  variables {
    locations = ["uksouth", "uksouth"]
  }

  expect_failures = [var.locations]
}

run "write_quorum_cannot_exceed_the_members" {
  command = plan

  variables {
    write_quorum = 3
  }

  expect_failures = [var.write_quorum]
}

run "app_principal_id_is_checked" {
  command = plan

  variables {
    app_principal_id = ""
  }

  expect_failures = [var.app_principal_id]
}
