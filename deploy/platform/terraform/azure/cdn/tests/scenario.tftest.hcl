# How the scenario composes modules/azure/storage and modules/azure/cdn,
# checked on a plan against mocked providers (decision 0030, S10). Each
# module's own baseline is its tests.

mock_provider "azurerm" {}
mock_provider "random" {}

# The account name, and so the rendered configuration, comes from the random
# suffix; the Front Door host is the endpoint's. A mocked provider would
# leave both unknown at plan.
override_resource {
  target          = random_string.suffix
  override_during = plan
  values = {
    result = "0a1b2c"
  }
}

override_resource {
  target          = module.cdn.azurerm_cdn_frontdoor_endpoint.cdn
  override_during = plan
  values = {
    id        = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/artifactweave-example-rg/providers/Microsoft.Cdn/profiles/artifactweave-example/afdEndpoints/artifactweave-example"
    host_name = "artifactweave-example-abcdefgh.z01.azurefd.net"
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
    app_ip_ranges    = ["203.0.113.7"]
  }

  assert {
    condition     = yamldecode(output.artifactweave_config) == yamldecode(one(regex("(?s)<!-- BEGIN_CONFIG_EXAMPLE -->\\n```yaml\\n(.*?)```\\n<!-- END_CONFIG_EXAMPLE -->", file("README.md"))))
    error_message = "the README's configuration example must be what its example inputs render"
  }
}

# A proof's instance as platform-scenarios.yml names it, longer than any it
# produces today (pr<number>-<run id>-<initials>-<step>), with
# scenario-ci.sh's NAME_PREFIX, the tags it sets, the scenarios resource
# group the deployer's rights end at, and the runner's address.
run "ci_run" {
  command = plan

  variables {
    name                = "awscn-pr1234-123456789012-ac-t"
    resource_group_name = "scenarios-rg"
    app_ip_ranges       = ["203.0.113.7"]
    tags = {
      artifactweave-scenario = "cdn"
      instance               = "pr1234-123456789012-ac-t"
      expires-at             = "1767225600"
    }
  }

  override_resource {
    target          = module.cdn.azurerm_cdn_frontdoor_endpoint.cdn
    override_during = plan
    values = {
      id        = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/scenarios-rg/providers/Microsoft.Cdn/profiles/awscn-pr1234-123456789012-ac-t/afdEndpoints/awscn-pr1234-123456789012-ac-t"
      host_name = "awscn-pr1234-123456789012-ac-t-abcdefgh.z01.azurefd.net"
    }
  }

  assert {
    condition     = output.account == "awscnpr123412340a1b2cuks" && can(regex("^[a-z0-9]{3,24}$", output.account))
    error_message = "a CI instance's account must fit Azure's 24 lowercase letters and digits"
  }

  assert {
    condition     = length(azurerm_resource_group.this) == 0 && output.resource_group == "scenarios-rg"
    error_message = "a CI instance must live in the scenarios resource group, where the deployer's rights end"
  }

  assert {
    condition     = output.front_door_profile == "awscn-pr1234-123456789012-ac-t" && output.storage_host == "awscn-pr1234-123456789012-ac-t-abcdefgh.z01.azurefd.net"
    error_message = "the profile must be named after the instance, and artifactweave must sign for its endpoint's host"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "expires_at must be the expires-at tag, which reap reads from the state"
  }

  assert {
    condition     = module.storage.network_access.public == "Enabled" && module.storage.network_access.ip_rules == tolist(["203.0.113.7"]) && length(module.storage.network_access.subnet_ids) == 0
    error_message = "the account's public endpoint must answer the runner's address alone"
  }
}

run "with_no_app_networks_the_public_endpoint_is_off" {
  command = plan

  assert {
    condition     = module.storage.network_access.public == "Disabled"
    error_message = "with no app_ip_ranges or app_subnet_ids, only private endpoints, Front Door's among them, may reach the account"
  }
}

run "app_networks_alone_reach_the_public_endpoint" {
  command = plan

  variables {
    app_ip_ranges  = ["203.0.113.7"]
    app_subnet_ids = ["/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/net/providers/Microsoft.Network/virtualNetworks/vnet/subnets/app"]
  }

  assert {
    condition = module.storage.network_access.public == "Enabled" && module.storage.network_access.ip_rules == tolist(["203.0.113.7"]) && (
      module.storage.network_access.subnet_ids == tolist(["/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/net/providers/Microsoft.Network/virtualNetworks/vnet/subnets/app"])
    )
    error_message = "with app networks, the public endpoint must take requests from them alone"
  }
}

run "the_ttl_sets_both_the_urls_and_the_edge_cap" {
  command = plan

  variables {
    redirect_ttl_minutes = 10
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).download.redirect.ttl == "10m" && module.cdn.cache_duration == "00:10:00"
    error_message = "redirect_ttl_minutes must set artifactweave's download.redirect.ttl and the edge's cache cap together"
  }
}

run "the_prefix_reaches_the_config_and_the_route" {
  command = plan

  variables {
    container    = "blobs"
    store_prefix = "local/"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.azure.prefix == "local/" && module.cdn.blob_path == "/blobs/local/sha256/"
    error_message = "container and store_prefix must reach blob.azure and the route, so Front Door serves the blobs artifactweave writes"
  }
}

run "a_domain_becomes_the_front_door_endpoint" {
  command = plan

  variables {
    domain_name = "cdn.example.com"
    dns_zone_id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/dns-rg/providers/Microsoft.Network/dnsZones/example.com"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.azure.cdn.front_door.endpoint == "cdn.example.com" && output.storage_host == "cdn.example.com"
    error_message = "with domain_name, artifactweave must sign URLs for the custom host"
  }
}

run "the_resource_group_is_created_when_none_is_given" {
  command = plan

  variables {
    expiring_prefixes     = { "scratch/" = 1 }
    private_link_location = "ukwest"
    additional_principals = { operator = { principal_id = "00000000-0000-0000-0000-00000000000b", principal_type = "User" } }
  }

  assert {
    condition     = azurerm_resource_group.this[0].name == "artifactweave-example-rg" && output.resource_group == "artifactweave-example-rg"
    error_message = "with no resource_group_name, the scenario must create <name>-rg and put everything in it"
  }
}

run "the_name_fits_front_door" {
  command = plan

  variables {
    name = "a-name-much-longer-than-a-front-door-endpoint-allows"
  }

  expect_failures = [var.name]
}
