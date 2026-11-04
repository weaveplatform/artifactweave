# modules/azure/cdn

An Azure Front Door Premium profile in front of one
[`modules/azure/storage`](../storage/README.md) account, which it reaches over Private Link,
so the account's public endpoint can refuse the internet. Front Door checks no signature: it
passes artifactweave's SAS through to storage, which checks it, so the route keys its cache on
the whole query string and a rule caps how long an edge keeps a copy (decision
[0029](../../../../../../docs/research/decisions/0029-cdn-delivery-and-download-redirect.md)).
[`azure/cdn`](../../../azure/cdn/README.md) composes it with the storage module. The module
holds no provider block, so the caller's `azurerm` provider and subscription apply (decision
[0030](../../../../../../docs/research/decisions/0030-reference-deployments.md)).

Front Door asks the account for a private endpoint connection when the origin is created, and
the account's owner must approve it; the provider cannot. Until then the edge answers errors.
[`azure/cdn`'s Prerequisites](../../../azure/cdn/README.md#prerequisites) give the command.

## What it creates

- `azurerm_cdn_frontdoor_profile.cdn`: the Premium profile, `name`, tagged with `tags`.
- `azurerm_cdn_frontdoor_endpoint.cdn`: the endpoint, `name`, whose host is
  `<name>-<hash>.z01.azurefd.net`.
- `azurerm_cdn_frontdoor_origin_group.blobs`: one origin, so no health probes.
- `azurerm_cdn_frontdoor_origin.storage`: the account's blob endpoint, reached over Private
  Link in `private_link_location`, or the account's own region.
- `azurerm_cdn_frontdoor_rule_set.cache` and `azurerm_cdn_frontdoor_rule.cap`: a rule that keeps
  an edge copy for exactly `max_cache_age_minutes`.
- `azurerm_cdn_frontdoor_route.blobs`: serves `/<container>/<store_prefix>sha256/*` over
  HTTPS, keyed on the whole URL.
- With `domain_name`: `azurerm_cdn_frontdoor_custom_domain.cdn` with a managed certificate,
  `azurerm_cdn_frontdoor_custom_domain_association.cdn`, and in `dns_zone_id`,
  `azurerm_dns_txt_record.validation` (`_dnsauth.<host>`) and `azurerm_dns_cname_record.cdn`
  to the endpoint. Front Door validates the domain and issues the certificate asynchronously,
  which can take an hour; the endpoint's own host serves meanwhile.

## Invariants

- The account is reached over Private Link, so it can refuse the internet
  (`tests/security.tftest.hcl`, run `storage_is_reached_over_private_link`).
- The profile is Premium, the tier with Private Link origins (`tests/security.tftest.hcl`, run
  `the_profile_is_premium`).
- The origin's certificate is checked against its host, which Front Door sends as the Host
  header (`tests/security.tftest.hcl`, run `the_origin_certificate_is_checked`).
- The cache key is the whole URL, SAS included, so a copy cached for one signed URL is never
  served for the bare path (`tests/security.tftest.hcl`, run `the_cache_key_holds_the_sas`).
- An edge copy lives exactly `max_cache_age_minutes`, whatever the origin says, and the rule
  keeps the SAS in the cache key (`tests/security.tftest.hcl`, runs
  `a_rule_caps_edge_copies_at_the_ttl` and `the_route_applies_the_cap`).
- The route serves the committed blobs, `/<container>/<store_prefix>sha256/`, and nothing else
  in the account, with an empty origin path (`tests/security.tftest.hcl`, run
  `the_route_serves_committed_blobs_only`; `tests/inputs.tftest.hcl`, run
  `the_route_follows_the_container_and_prefix`).
- HTTPS only, at the edge and to the origin (`tests/security.tftest.hcl`, run
  `https_only_end_to_end`).
- A custom domain gets a Front Door managed certificate and refuses TLS older than 1.2
  (`tests/security.tftest.hcl`, run `a_custom_domain_has_a_managed_tls_1_2_certificate`).
- Tags reach the profile, the endpoint and the DNS records (`tests/security.tftest.hcl`, run
  `tags_reach_what_takes_them`).
- `max_cache_age_minutes` becomes Front Door's `HH:MM:SS` (`tests/inputs.tftest.hcl`, run
  `the_cache_duration_is_hours_and_minutes`).
- The Private Link request names the profile, so the connection to approve can be found by it
  (`tests/inputs.tftest.hcl`, run `the_request_names_the_profile`).
- Without `domain_name` the endpoint's own host serves and no domain or record exists; with it,
  the TXT and CNAME records land in the zone and the route serves the domain
  (`tests/inputs.tftest.hcl`, runs `without_a_domain_the_endpoint_host_serves`,
  `a_domain_gets_its_records_in_the_zone` and `a_domain_is_served_by_the_route`).

## Accepted findings

None: this module carries no `#trivy:ignore`, and trivy reports nothing on it.

## Usage

```hcl
module "storage" {
  source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/azure/storage?ref=v0.2.0" # x-release-please-version

  account_name          = "artifactweave0a1b2cuks"
  resource_group_name   = "artifactweave-rg"
  location              = "uksouth"
  principals            = { app = { principal_id = "00000000-0000-0000-0000-00000000000a" } }
  public_network_access = "Disabled"
}

module "cdn" {
  source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/azure/cdn?ref=v0.2.0" # x-release-please-version

  name                = "artifactweave"
  resource_group_name = "artifactweave-rg"
  origin              = { id = module.storage.id, host = module.storage.host, location = "uksouth" }
}
```

## Reference

Generated by terraform-docs (`make deploy-docs`).

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.9 |
| azurerm | ~> 5.9 |

### Providers

| Name | Version |
| ---- | ------- |
| azurerm | 5.9.0 |

### Modules

No modules.

### Resources

| Name | Type |
| ---- | ---- |
| [azurerm_cdn_frontdoor_custom_domain.cdn](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/cdn_frontdoor_custom_domain) | resource |
| [azurerm_cdn_frontdoor_custom_domain_association.cdn](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/cdn_frontdoor_custom_domain_association) | resource |
| [azurerm_cdn_frontdoor_endpoint.cdn](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/cdn_frontdoor_endpoint) | resource |
| [azurerm_cdn_frontdoor_origin.storage](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/cdn_frontdoor_origin) | resource |
| [azurerm_cdn_frontdoor_origin_group.blobs](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/cdn_frontdoor_origin_group) | resource |
| [azurerm_cdn_frontdoor_profile.cdn](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/cdn_frontdoor_profile) | resource |
| [azurerm_cdn_frontdoor_route.blobs](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/cdn_frontdoor_route) | resource |
| [azurerm_cdn_frontdoor_rule.cap](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/cdn_frontdoor_rule) | resource |
| [azurerm_cdn_frontdoor_rule_set.cache](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/cdn_frontdoor_rule_set) | resource |
| [azurerm_dns_cname_record.cdn](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/dns_cname_record) | resource |
| [azurerm_dns_txt_record.validation](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/dns_txt_record) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| container | The container artifactweave writes to (blob.azure.container), as given to modules/azure/storage. The route serves only the committed blobs under it. | `string` | `"artifactweave"` | no |
| dns\_zone\_id | Resource ID of the Azure DNS zone holding domain\_name, where the module writes the validation TXT record and the CNAME to the endpoint. Set it with domain\_name, and only then. | `string` | `null` | no |
| domain\_name | A host of your own for the endpoint, such as cdn.example.com, in the Azure DNS zone dns\_zone\_id, served with a Front Door managed certificate. Make it a sibling of artifactweave's host, never a subdomain of it, so clients drop their credentials on the redirect; artifactweave refuses to start otherwise. Null serves from the endpoint's own *.azurefd.net host. | `string` | `null` | no |
| max\_cache\_age\_minutes | How long an edge keeps a blob's response, in minutes. Set it to artifactweave's download.redirect.ttl: a signed URL then stops being served at most this long after its SAS expires (decision 0029). | `number` | `5` | no |
| name | Name of the Front Door profile and its endpoint: 1 to 46 lowercase letters, digits and hyphens. The endpoint's host is <name>-<hash>.z01.azurefd.net, so the name need only be unique in the resource group. | `string` | n/a | yes |
| origin | The storage account Front Door reads from over Private Link: modules/azure/storage's id and host outputs, and the account's location. | ```object({ id = string host = string location = string })``` | n/a | yes |
| private\_link\_location | Region where Front Door's private endpoint to the account terminates; it must be one where Front Door offers Private Link origins. Null uses the account's location, which keeps the link closest to the origin. | `string` | `null` | no |
| resource\_group\_name | Resource group to create the profile in. | `string` | n/a | yes |
| store\_prefix | Blob prefix artifactweave writes under (blob.azure.prefix), as given to modules/azure/storage. The route serves only the committed blobs, /<container>/<store\_prefix>sha256/. | `string` | `""` | no |
| tags | Tags for the profile, the endpoint and the DNS records, the resources here that take them. | `map(string)` | `{}` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| blob\_path | The path the endpoint serves blobs under, /<container>/<store\_prefix>sha256/; any other path answers 404 at the edge. |
| cache\_duration | How long an edge keeps a copy, as Front Door's rule writes it (HH:MM:SS). |
| domain | Where artifactweave's Front Door URLs point (blob.azure.cdn.front\_door.endpoint): domain\_name, or the endpoint's own *.azurefd.net host. |
| endpoint\_host | The endpoint's own *.azurefd.net host, which domain\_name's CNAME points at. |
| private\_link\_request\_message | The request Front Door's private endpoint connection carries on the storage account, by which the connection to approve is found. |
| profile\_id | The Front Door profile's resource ID. |
| profile\_name | The Front Door profile's name, for the portal and CLI. |
<!-- END_TF_DOCS -->
