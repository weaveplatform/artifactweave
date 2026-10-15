# The bootstrap's invariants, checked on a plan against a mocked provider: no
# credentials and no calls to Azure (decision 0030). The zone's
# prevent_destroy is a lifecycle setting a test cannot read; Terraform itself
# enforces it on any plan that would destroy the zone.

mock_provider "azurerm" {
  mock_data "azurerm_client_config" {
    defaults = {
      tenant_id = "2fd6bb84-ad40-4ec5-9369-a215b25c9952"
      object_id = "40b18b93-ae0d-45f5-98fe-6579fc792faa"
    }
  }
  mock_resource "azurerm_user_assigned_identity" {
    defaults = {
      principal_id = "00000000-0000-0000-0000-000000000001"
    }
  }
}

# A mocked provider cannot import, so the two resources imports.tf adopts are
# overridden with the state an import would read.
override_resource {
  target = azurerm_resource_group.bootstrap
  values = {
    id = "/subscriptions/c33dbc35-f86d-4b7c-a81e-2a33eb3f7946/resourceGroups/artifactweave-bootstrap-rg"
  }
}

override_resource {
  target = azurerm_dns_zone.azure
  values = {
    id           = "/subscriptions/c33dbc35-f86d-4b7c-a81e-2a33eb3f7946/resourceGroups/artifactweave-bootstrap-rg/providers/Microsoft.Network/dnsZones/azure.artifact.weaveplatform.run"
    name_servers = ["ns1-06.azure-dns.com."]
  }
}

# Known IDs for the resources whose IDs the deployer's scopes are compared
# against; a mocked provider would otherwise leave them unknown at plan.
override_resource {
  target          = azurerm_resource_group.scenarios
  override_during = plan
  values = {
    id = "/subscriptions/c33dbc35-f86d-4b7c-a81e-2a33eb3f7946/resourceGroups/artifactweave-scenarios-rg"
  }
}

run "trust_pins_the_immutable_subject_per_environment" {
  command = plan

  assert {
    condition     = azurerm_federated_identity_credential.conformance.subject == "repo:weaveplatform@336817058/artifactweave@1415179278:environment:cloud-conformance"
    error_message = "the conformance identity must trust the cloud-conformance subject only"
  }

  assert {
    condition     = azurerm_federated_identity_credential.deployer.subject == "repo:weaveplatform@336817058/artifactweave@1415179278:environment:cloud-platform"
    error_message = "the deployer identity must trust the cloud-platform subject only"
  }

  assert {
    condition = alltrue([
      for c in [azurerm_federated_identity_credential.conformance, azurerm_federated_identity_credential.deployer] :
      c.issuer == "https://token.actions.githubusercontent.com" && c.audience == tolist(["api://AzureADTokenExchange"])
    ])
    error_message = "both credentials must trust GitHub's issuer with Entra's token-exchange audience"
  }
}

run "deployer_stays_inside_the_scenarios_group" {
  command = plan

  assert {
    condition = alltrue([
      for a in [azurerm_role_assignment.deployer_contributor, azurerm_role_assignment.deployer_blob_data, azurerm_role_assignment.deployer_grants_blob_roles] :
      a.scope == azurerm_resource_group.scenarios.id
    ])
    error_message = "the deployer's management rights must stop at the scenarios resource group"
  }

  assert {
    condition     = strcontains(azurerm_role_assignment.deployer_grants_blob_roles.condition, "ba92f5b4-2d11-453d-a403-e96b0029c9fe, 2a2b9908-6ea1-4ae2-8e65-a410df84e7d1") && azurerm_role_assignment.deployer_grants_blob_roles.condition_version == "2.0"
    error_message = "the deployer may assign the blob data roles only"
  }
}

run "state_is_private_and_versioned" {
  command = plan

  assert {
    condition     = azurerm_storage_account.state.shared_access_key_enabled == false && azurerm_storage_account.state.allow_nested_items_to_be_public == false
    error_message = "the state account must take Entra ID only and allow no public containers"
  }

  assert {
    condition     = azurerm_storage_account.state.blob_properties[0].versioning_enabled
    error_message = "the state account must keep blob versions, so a bad apply can be undone"
  }

  assert {
    condition     = azurerm_storage_container.state.container_access_type == "private"
    error_message = "the state container must be private"
  }

  assert {
    condition     = azurerm_storage_account.state.name == "awtfstatec33dbc35"
    error_message = "the state account's name must match backend.tf"
  }
}
