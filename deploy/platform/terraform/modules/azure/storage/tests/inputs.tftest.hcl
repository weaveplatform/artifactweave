# How the module reads its inputs, on a mocked plan: replication, the
# container, the prefix normalisation the backend applies, the checks that
# fail at plan rather than at apply, and the defaults a consumer gets.

mock_provider "azurerm" {}

variables {
  account_name        = "example0a1b2cuks"
  resource_group_name = "example-rg"
  location            = "uksouth"
  principals          = { app = { principal_id = "00000000-0000-0000-0000-00000000000a" } }
}

run "the_account_keeps_one_region_by_default" {
  command = plan

  assert {
    condition     = azurerm_storage_account.blobs.account_replication_type == "LRS"
    error_message = "the account must be LRS unless asked otherwise: a mirror member keeps one region's copy"
  }
}

run "replication_type_reaches_the_account" {
  command = plan

  variables {
    replication_type = "RAGZRS"
  }

  assert {
    condition     = azurerm_storage_account.blobs.account_replication_type == "RAGZRS"
    error_message = "replication_type must set the account's replication"
  }
}

run "the_container_is_named_as_given" {
  command = plan

  variables {
    container = "blobs"
  }

  assert {
    condition     = azurerm_storage_container.blobs.name == "blobs" && output.container == "blobs"
    error_message = "container must name the account's container"
  }
}

run "store_prefix_defaults_to_the_container_root" {
  command = plan

  assert {
    condition     = azurerm_storage_management_policy.cleanup.rule[0].filters[0].prefix_match == toset(["artifactweave/_uploads/"])
    error_message = "with no store_prefix, temporary blobs live under _uploads/ at the container's root"
  }
}

run "store_prefix_gains_a_trailing_slash" {
  command = plan

  variables {
    store_prefix = "local"
  }

  assert {
    condition     = azurerm_storage_management_policy.cleanup.rule[0].filters[0].prefix_match == toset(["artifactweave/local/_uploads/"])
    error_message = "store_prefix must be normalised as the backend does, with a trailing slash"
  }
}

run "store_prefix_loses_a_leading_slash" {
  command = plan

  variables {
    store_prefix = "/local/"
  }

  assert {
    condition     = azurerm_storage_management_policy.cleanup.rule[0].filters[0].prefix_match == toset(["artifactweave/local/_uploads/"])
    error_message = "store_prefix must be normalised as the backend does, without a leading slash"
  }
}

run "outputs_name_the_account_and_its_signed_urls" {
  command = plan

  assert {
    condition = [output.account, output.container, output.host, output.signed_url_prefix] == [
      "example0a1b2cuks", "artifactweave", "example0a1b2cuks.blob.core.windows.net",
      "example0a1b2cuks.blob.core.windows.net/artifactweave/",
    ]
    error_message = "the outputs must name the account, its container, and the blob endpoint signed URLs use"
  }
}

run "a_readable_copy_has_a_secondary_host" {
  command = plan

  variables {
    replication_type = "RAGZRS"
  }

  assert {
    condition     = output.secondary_host == "example0a1b2cuks-secondary.blob.core.windows.net"
    error_message = "with a readable geo copy, secondary_host must be the paired region's endpoint, <account>-secondary"
  }
}

run "an_unreadable_copy_has_no_secondary_host" {
  command = plan

  variables {
    replication_type = "GZRS"
  }

  assert {
    condition     = output.secondary_host == null
    error_message = "a GZRS or GRS copy is readable only after a failover, so secondary_host must be null"
  }
}

run "account_name_is_checked" {
  command = plan

  variables {
    account_name = "Not-A-Valid-Name"
  }

  expect_failures = [var.account_name]
}

run "replication_type_is_checked" {
  command = plan

  variables {
    replication_type = "GEO"
  }

  expect_failures = [var.replication_type]
}

run "container_is_checked" {
  command = plan

  variables {
    container = "two--hyphens"
  }

  expect_failures = [var.container]
}

run "principal_ids_are_guids" {
  command = plan

  variables {
    principals = { app = { principal_id = "someone@example.com" } }
  }

  expect_failures = [var.principals]
}

run "principal_types_are_checked" {
  command = plan

  variables {
    principals = { app = { principal_id = "00000000-0000-0000-0000-00000000000a", principal_type = "Robot" } }
  }

  expect_failures = [var.principals]
}

run "expiring_prefix_days_are_whole_and_positive" {
  command = plan

  variables {
    expiring_prefixes = { "scratch/" = 0 }
  }

  expect_failures = [var.expiring_prefixes]
}
