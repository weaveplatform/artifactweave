# What expires, and when (decision 0030), on a mocked plan. Each rule is
# checked at its position in main.tf.

mock_provider "azurerm" {}

variables {
  account_name        = "example0a1b2cuks"
  resource_group_name = "example-rg"
  location            = "uksouth"
  principals          = { app = { principal_id = "00000000-0000-0000-0000-00000000000a" } }
  store_prefix        = "local/"
}

run "abandoned_uploads_expire_after_two_days" {
  command = plan

  assert {
    condition = (
      azurerm_storage_management_policy.cleanup.rule[0].enabled &&
      azurerm_storage_management_policy.cleanup.rule[0].filters[0].prefix_match == toset(["artifactweave/local/_uploads/"]) &&
      azurerm_storage_management_policy.cleanup.rule[0].actions[0].base_blob[0].delete_after_days_since_modification_greater_than == 2
    )
    error_message = "the first rule must delete the temporary blobs an interrupted upload leaves under <store_prefix>_uploads/ after 2 days"
  }
}

run "expiring_prefixes_expire_after_their_days" {
  command = plan

  variables {
    expiring_prefixes = { "conformance/" = 1, "scratch/" = 7 }
  }

  assert {
    condition = [
      for r in slice(azurerm_storage_management_policy.cleanup.rule, 1, 3) :
      "${r.name} ${one(r.filters[0].prefix_match)} ${r.actions[0].base_blob[0].delete_after_days_since_modification_greater_than}"
    ] == ["expire-conformance artifactweave/conformance/ 1", "expire-scratch artifactweave/scratch/ 7"]
    error_message = "each expiring prefix must get a rule deleting its blobs in the container after its days"
  }
}
