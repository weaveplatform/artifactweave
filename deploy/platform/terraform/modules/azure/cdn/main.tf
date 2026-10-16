# An Azure Front Door Premium profile in front of one modules/azure/storage
# account, which it reaches over Private Link, so the account's public
# endpoint can refuse the internet (decision 0029). Front Door checks no
# signature: it passes artifactweave's SAS through to storage, which checks
# it, so the route keys its cache on the whole query string and a rule caps
# how long an edge keeps a copy. azure/cdn composes it; it holds no provider
# block, so the caller's azurerm provider and subscription apply
# (decision 0030).

locals {
  custom = var.domain_name != null

  # As the azure backend normalises blob.azure.prefix, so the route serves
  # the blobs it writes.
  trimmed      = trimprefix(var.store_prefix, "/")
  store_prefix = local.trimmed == "" || endswith(local.trimmed, "/") ? local.trimmed : "${local.trimmed}/"

  # Front Door's rules take a duration as HH:MM:SS below a day.
  cache_duration = format("%02d:%02d:00", floor(var.max_cache_age_minutes / 60), var.max_cache_age_minutes % 60)

  # Storage records the request on the private endpoint connection it
  # creates, which is how the connection to approve is told apart.
  request_message = "artifactweave Front Door ${var.name} reads blobs"

  # /resourceGroups/<group>/providers/Microsoft.Network/dnsZones/<zone>
  zone      = local.custom ? regex("(?i)/resourceGroups/([^/]+)/providers/Microsoft\\.Network/dnsZones/([^/]+)$", var.dns_zone_id) : []
  zone_name = local.custom ? local.zone[1] : null
  subdomain = local.custom ? trimsuffix(var.domain_name, ".${local.zone_name}") : null
}

resource "azurerm_cdn_frontdoor_profile" "cdn" {
  name                = var.name
  resource_group_name = var.resource_group_name
  # Premium is the tier that reaches an origin over Private Link.
  sku_name = "Premium_AzureFrontDoor"
  tags     = var.tags
}

resource "azurerm_cdn_frontdoor_endpoint" "cdn" {
  name                     = var.name
  cdn_frontdoor_profile_id = azurerm_cdn_frontdoor_profile.cdn.id
  tags                     = var.tags
}

# One origin, so no health probes: they would only add requests to storage.
resource "azurerm_cdn_frontdoor_origin_group" "blobs" {
  name                     = "blobs"
  cdn_frontdoor_profile_id = azurerm_cdn_frontdoor_profile.cdn.id

  load_balancing {}
}

# Front Door asks the account for a private endpoint connection when the
# origin is created, and the account's owner approves it: the provider cannot
# (README, Prerequisites). Until then the edge answers errors.
resource "azurerm_cdn_frontdoor_origin" "storage" {
  name                          = "storage"
  cdn_frontdoor_origin_group_id = azurerm_cdn_frontdoor_origin_group.blobs.id
  host_name                     = var.origin.host
  origin_host_header            = var.origin.host
  # Private Link needs the origin's certificate checked against its host.
  certificate_name_check_enabled = true

  private_link {
    target_type            = "blob"
    private_link_target_id = var.origin.id
    location               = coalesce(var.private_link_location, var.origin.location)
    request_message        = local.request_message
  }
}

# The cap on an edge copy (decision 0029): artifactweave's SAS carries
# max-age=<TTL>, and this rule makes the edge keep a response for exactly
# max_cache_age_minutes whatever the origin says, so an expired SAS stops
# being served once its copy ages out. The override repeats UseQueryString,
# because a rule's caching replaces the route's whole cache configuration.
resource "azurerm_cdn_frontdoor_rule_set" "cache" {
  name                     = "artifactweavecache"
  cdn_frontdoor_profile_id = azurerm_cdn_frontdoor_profile.cdn.id
}

resource "azurerm_cdn_frontdoor_rule" "cap" {
  name                      = "capedgecopies"
  cdn_frontdoor_rule_set_id = azurerm_cdn_frontdoor_rule_set.cache.id
  order                     = 1
  behaviour_on_match        = "Continue"

  actions {
    route_configuration_override {
      caching {
        behaviour              = "OverrideAlways"
        duration               = local.cache_duration
        query_string_behaviour = "UseQueryString"
        compression_enabled    = false
      }
    }
  }

  # The provider requires it: a rule is deleted before the origin it may
  # route to.
  depends_on = [azurerm_cdn_frontdoor_origin_group.blobs, azurerm_cdn_frontdoor_origin.storage]
}

# The route serves the committed blobs under the store's prefix and nothing
# else in the account. The cache key is the whole URL, SAS included: under
# Front Door's default, which ignores the query string, a copy cached for one
# signed URL would be served to anyone asking for the path (decision 0029).
# The origin path stays empty, so artifactweave's
# blob.azure.cdn.front_door.path_prefix does too.
resource "azurerm_cdn_frontdoor_route" "blobs" {
  name                            = "blobs"
  cdn_frontdoor_endpoint_id       = azurerm_cdn_frontdoor_endpoint.cdn.id
  cdn_frontdoor_origin_group_id   = azurerm_cdn_frontdoor_origin_group.blobs.id
  cdn_frontdoor_origin_ids        = [azurerm_cdn_frontdoor_origin.storage.id]
  cdn_frontdoor_rule_set_ids      = [azurerm_cdn_frontdoor_rule_set.cache.id]
  cdn_frontdoor_custom_domain_ids = local.custom ? [azurerm_cdn_frontdoor_custom_domain.cdn[0].id] : null
  link_to_default_domain          = true
  patterns_to_match               = ["/${var.container}/${local.store_prefix}sha256/*"]
  supported_protocols             = ["Https"]
  https_redirect_enabled          = false
  forwarding_protocol             = "HttpsOnly"

  cache {
    query_string_caching_behavior = "UseQueryString"
    compression_enabled           = false
  }

  depends_on = [azurerm_cdn_frontdoor_rule.cap]
}

# With domain_name: the host, a Front Door managed certificate, the TXT
# record that proves the zone is ours, and the CNAME to the endpoint. Front
# Door validates the domain and issues the certificate asynchronously, which
# can take an hour; the endpoint's own host serves meanwhile.
resource "azurerm_cdn_frontdoor_custom_domain" "cdn" {
  count                    = local.custom ? 1 : 0
  name                     = replace(var.domain_name, ".", "-")
  cdn_frontdoor_profile_id = azurerm_cdn_frontdoor_profile.cdn.id
  dns_zone_id              = var.dns_zone_id
  host_name                = var.domain_name

  tls {
    certificate_type = "ManagedCertificate"
    minimum_version  = "TLS12"
  }
}

resource "azurerm_cdn_frontdoor_custom_domain_association" "cdn" {
  count                          = local.custom ? 1 : 0
  cdn_frontdoor_custom_domain_id = azurerm_cdn_frontdoor_custom_domain.cdn[0].id
  cdn_frontdoor_route_ids        = [azurerm_cdn_frontdoor_route.blobs.id]
}

resource "azurerm_dns_txt_record" "validation" {
  count               = local.custom ? 1 : 0
  name                = "_dnsauth.${local.subdomain}"
  zone_name           = local.zone_name
  resource_group_name = local.zone[0]
  ttl                 = 3600
  tags                = var.tags

  record {
    value = azurerm_cdn_frontdoor_custom_domain.cdn[0].validation_token
  }
}

# After the route, so the host never points at an endpoint that does not
# serve it yet.
resource "azurerm_dns_cname_record" "cdn" {
  count               = local.custom ? 1 : 0
  name                = local.subdomain
  zone_name           = local.zone_name
  resource_group_name = local.zone[0]
  ttl                 = 3600
  record              = azurerm_cdn_frontdoor_endpoint.cdn.host_name
  tags                = var.tags

  depends_on = [azurerm_cdn_frontdoor_custom_domain_association.cdn]
}
