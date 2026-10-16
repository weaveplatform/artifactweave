# The profile's security baseline (decision 0029), checked on a plan against
# a mocked provider: no credentials and no calls to Azure. Scenarios that
# call this module check how they compose it, not these again. One assertion
# per run, so a failure names the invariant it breaks.

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

run "storage_is_reached_over_private_link" {
  command = plan

  assert {
    condition = [for p in azurerm_cdn_frontdoor_origin.storage.private_link : [p.target_type, p.private_link_target_id]] == [[
      "blob", "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example-rg/providers/Microsoft.Storage/storageAccounts/example0a1b2cuks",
    ]]
    error_message = "the origin must reach the account's blob endpoint through Private Link, so the account can refuse the internet"
  }
}

run "the_profile_is_premium" {
  command = plan

  assert {
    condition     = azurerm_cdn_frontdoor_profile.cdn.sku_name == "Premium_AzureFrontDoor"
    error_message = "the profile must be Premium, the tier that reaches an origin over Private Link"
  }
}

run "the_origin_certificate_is_checked" {
  command = plan

  assert {
    condition     = azurerm_cdn_frontdoor_origin.storage.certificate_name_check_enabled && azurerm_cdn_frontdoor_origin.storage.origin_host_header == "example0a1b2cuks.blob.core.windows.net"
    error_message = "Front Door must check the account's certificate against its host, which it sends as the Host header"
  }
}

run "the_cache_key_holds_the_sas" {
  command = plan

  assert {
    condition     = [for c in azurerm_cdn_frontdoor_route.blobs.cache : c.query_string_caching_behavior] == ["UseQueryString"]
    error_message = "the route must key its cache on the whole query string, or a copy cached for one signed URL is served to anyone asking for the path"
  }
}

run "a_rule_caps_edge_copies_at_the_ttl" {
  command = plan

  assert {
    condition = [for a in azurerm_cdn_frontdoor_rule.cap.actions : [
      for o in a.route_configuration_override : [
        for c in o.caching : [c.behaviour, c.duration, c.query_string_behaviour]
      ]
    ]] == [[[["OverrideAlways", "00:05:00", "UseQueryString"]]]]
    error_message = "a rule must keep an edge copy for exactly the TTL, five minutes by default, and keep the SAS in the cache key"
  }
}

run "the_route_applies_the_cap" {
  command = plan

  override_resource {
    target          = azurerm_cdn_frontdoor_rule_set.cache
    override_during = plan
    values = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example-rg/providers/Microsoft.Cdn/profiles/example-cdn/ruleSets/artifactweavecache"
    }
  }

  assert {
    condition     = azurerm_cdn_frontdoor_route.blobs.cdn_frontdoor_rule_set_ids == toset(["/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example-rg/providers/Microsoft.Cdn/profiles/example-cdn/ruleSets/artifactweavecache"])
    error_message = "the route must run the rule set that caps edge copies"
  }
}

run "the_route_serves_committed_blobs_only" {
  command = plan

  assert {
    condition     = azurerm_cdn_frontdoor_route.blobs.patterns_to_match == tolist(["/artifactweave/sha256/*"]) && azurerm_cdn_frontdoor_route.blobs.cdn_frontdoor_origin_path == null
    error_message = "the route must serve the container's committed blobs, /<container>/sha256/, and nothing else in the account, with an empty origin path"
  }
}

run "https_only_end_to_end" {
  command = plan

  assert {
    condition     = azurerm_cdn_frontdoor_route.blobs.supported_protocols == toset(["Https"]) && azurerm_cdn_frontdoor_route.blobs.forwarding_protocol == "HttpsOnly"
    error_message = "the edge must take HTTPS only and reach the origin over HTTPS only, since a SAS in the URL is a credential"
  }
}

run "a_custom_domain_has_a_managed_tls_1_2_certificate" {
  command = plan

  variables {
    domain_name = "cdn.example.com"
    dns_zone_id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/dns-rg/providers/Microsoft.Network/dnsZones/example.com"
  }

  assert {
    condition     = [for t in azurerm_cdn_frontdoor_custom_domain.cdn[0].tls : [t.certificate_type, t.minimum_version]] == [["ManagedCertificate", "TLS12"]]
    error_message = "a custom domain must use a Front Door managed certificate and refuse TLS older than 1.2"
  }
}

run "tags_reach_what_takes_them" {
  command = plan

  variables {
    tags        = { artifactweave-scenario = "cdn", instance = "r1", expires-at = "1767225600" }
    domain_name = "cdn.example.com"
    dns_zone_id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/dns-rg/providers/Microsoft.Network/dnsZones/example.com"
  }

  assert {
    condition = alltrue([for t in [
      azurerm_cdn_frontdoor_profile.cdn.tags,
      azurerm_cdn_frontdoor_endpoint.cdn.tags,
      azurerm_dns_txt_record.validation[0].tags,
      azurerm_dns_cname_record.cdn[0].tags,
    ] : t == tomap({ artifactweave-scenario = "cdn", instance = "r1", expires-at = "1767225600" })])
    error_message = "the profile, the endpoint and the DNS records must carry the tags given, which reap finds them by"
  }
}
