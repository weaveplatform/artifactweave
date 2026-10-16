# How the module reads its inputs, on a mocked plan: the prefix
# normalisation the backend applies, the cache duration's format, the Private
# Link location, the custom domain's records, and the checks that fail at
# plan rather than at apply.

mock_provider "azurerm" {}

variables {
  name                = "example-cdn"
  resource_group_name = "example-rg"
  origin = {
    id       = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example-rg/providers/Microsoft.Storage/storageAccounts/example0a1b2cuks"
    host     = "example0a1b2cuks.blob.core.windows.net"
    location = "uksouth"
  }
}

run "names_reach_the_profile_and_endpoint" {
  command = plan

  assert {
    condition     = azurerm_cdn_frontdoor_profile.cdn.name == "example-cdn" && azurerm_cdn_frontdoor_endpoint.cdn.name == "example-cdn" && azurerm_cdn_frontdoor_profile.cdn.resource_group_name == "example-rg"
    error_message = "name must name the profile and the endpoint, in resource_group_name"
  }
}

run "the_route_follows_the_container_and_prefix" {
  command = plan

  variables {
    container    = "blobs"
    store_prefix = "/local"
  }

  assert {
    condition     = azurerm_cdn_frontdoor_route.blobs.patterns_to_match == tolist(["/blobs/local/sha256/*"])
    error_message = "the route must serve <container>/<store_prefix>sha256/, the prefix normalised as the backend does"
  }
}

run "the_cache_duration_is_hours_and_minutes" {
  command = plan

  variables {
    max_cache_age_minutes = 90
  }

  assert {
    condition     = one(one(one(azurerm_cdn_frontdoor_rule.cap.actions).route_configuration_override).caching).duration == "01:30:00"
    error_message = "max_cache_age_minutes must become Front Door's HH:MM:SS"
  }
}

run "private_link_terminates_beside_the_account" {
  command = plan

  assert {
    condition     = one(azurerm_cdn_frontdoor_origin.storage.private_link).location == "uksouth"
    error_message = "with no private_link_location, the link must terminate in the account's own region"
  }
}

run "private_link_location_moves_it" {
  command = plan

  variables {
    private_link_location = "ukwest"
  }

  assert {
    condition     = one(azurerm_cdn_frontdoor_origin.storage.private_link).location == "ukwest"
    error_message = "private_link_location must set where the link terminates"
  }
}

run "the_request_names_the_profile" {
  command = plan

  assert {
    condition     = one(azurerm_cdn_frontdoor_origin.storage.private_link).request_message == output.private_link_request_message && strcontains(output.private_link_request_message, "example-cdn")
    error_message = "the private link request must name the profile, so the connection to approve can be found by it"
  }
}

run "without_a_domain_the_endpoint_host_serves" {
  command = plan

  override_resource {
    target          = azurerm_cdn_frontdoor_endpoint.cdn
    override_during = plan
    values = {
      id        = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example-rg/providers/Microsoft.Cdn/profiles/example-cdn/afdEndpoints/example-cdn"
      host_name = "example-cdn-abcdefgh.z01.azurefd.net"
    }
  }

  assert {
    condition     = output.domain == "example-cdn-abcdefgh.z01.azurefd.net" && length(azurerm_cdn_frontdoor_custom_domain.cdn) == 0 && length(azurerm_dns_cname_record.cdn) == 0
    error_message = "with no domain_name, the endpoint's own host must serve and no domain or record be created"
  }
}

run "a_domain_gets_its_records_in_the_zone" {
  command = plan

  variables {
    domain_name = "cdn.sub.example.com"
    dns_zone_id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/dns-rg/providers/Microsoft.Network/dnsZones/sub.example.com"
  }

  override_resource {
    target          = azurerm_cdn_frontdoor_endpoint.cdn
    override_during = plan
    values = {
      id        = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example-rg/providers/Microsoft.Cdn/profiles/example-cdn/afdEndpoints/example-cdn"
      host_name = "example-cdn-abcdefgh.z01.azurefd.net"
    }
  }

  assert {
    condition = [
      azurerm_dns_txt_record.validation[0].name, azurerm_dns_txt_record.validation[0].zone_name, azurerm_dns_txt_record.validation[0].resource_group_name,
      azurerm_dns_cname_record.cdn[0].name, azurerm_dns_cname_record.cdn[0].record, output.domain,
      ] == [
      "_dnsauth.cdn", "sub.example.com", "dns-rg",
      "cdn", "example-cdn-abcdefgh.z01.azurefd.net", "cdn.sub.example.com",
    ]
    error_message = "domain_name must get its validation TXT record and a CNAME to the endpoint in the zone, and become the domain artifactweave signs for"
  }
}

run "a_domain_is_served_by_the_route" {
  command = plan

  variables {
    domain_name = "cdn.example.com"
    dns_zone_id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/dns-rg/providers/Microsoft.Network/dnsZones/example.com"
  }

  override_resource {
    target          = azurerm_cdn_frontdoor_custom_domain.cdn
    override_during = plan
    values = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example-rg/providers/Microsoft.Cdn/profiles/example-cdn/customDomains/cdn-example-com"
    }
  }

  assert {
    condition     = azurerm_cdn_frontdoor_route.blobs.cdn_frontdoor_custom_domain_ids == toset(["/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example-rg/providers/Microsoft.Cdn/profiles/example-cdn/customDomains/cdn-example-com"]) && azurerm_cdn_frontdoor_custom_domain_association.cdn[0].cdn_frontdoor_custom_domain_id == azurerm_cdn_frontdoor_custom_domain.cdn[0].id
    error_message = "the route must serve the custom domain, and the association must link them"
  }
}

run "name_is_checked" {
  command = plan

  variables {
    name = "Example_CDN"
  }

  expect_failures = [var.name]
}

run "origin_is_a_storage_account" {
  command = plan

  variables {
    origin = {
      id       = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example-rg/providers/Microsoft.Web/sites/example"
      host     = "example.azurewebsites.net"
      location = "uksouth"
    }
  }

  expect_failures = [var.origin]
}

run "max_cache_age_is_under_a_day" {
  command = plan

  variables {
    max_cache_age_minutes = 1440
  }

  expect_failures = [var.max_cache_age_minutes]
}

run "domain_name_is_checked" {
  command = plan

  variables {
    domain_name = "CDN.example.com"
    dns_zone_id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/dns-rg/providers/Microsoft.Network/dnsZones/example.com"
  }

  expect_failures = [var.domain_name]
}

run "a_domain_needs_its_zone" {
  command = plan

  variables {
    domain_name = "cdn.example.com"
  }

  expect_failures = [var.dns_zone_id]
}

run "the_domain_is_in_the_zone" {
  command = plan

  variables {
    domain_name = "cdn.example.org"
    dns_zone_id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/dns-rg/providers/Microsoft.Network/dnsZones/example.com"
  }

  expect_failures = [var.dns_zone_id]
}
