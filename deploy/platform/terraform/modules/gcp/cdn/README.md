# modules/gcp/cdn

Cloud CDN in front of one [`modules/gcp/storage`](../storage/README.md) bucket, serving
artifactweave's blobs to URLs artifactweave signs (decision
[0029](../../../../../../docs/research/decisions/0029-cdn-delivery-and-download-redirect.md)):
a backend bucket with Cloud CDN behind a global external Application Load Balancer, with a
Google-managed certificate for `domain_name` from Certificate Manager.
[`gcp/cdn`](../../../gcp/cdn/README.md) composes it with the storage module. The module holds
no provider block; the caller's `google` provider and project apply (decision
[0030](../../../../../../docs/research/decisions/0030-reference-deployments.md)).

Cloud CDN refuses a bad or expired signature but forwards an unsigned request to the bucket,
so the bucket stays private and this module grants Cloud CDN's fill account,
`service-<project number>@cloud-cdn-fill.iam.gserviceaccount.com`, read access to the
committed blobs. Google creates that account when a project's first signed URL key is added,
and the key is added with `gcloud` after apply so it never reaches the state, so the grant
waits for `grant_cdn_fill`. IAM members are not authoritative, so the grant sits beside the
storage module's own.

## What it creates

- `google_compute_backend_bucket.cdn`: the bucket with Cloud CDN on, uncompressed, caching
  signed responses for `signed_url_cache_max_age_sec` and no error responses, with the two
  response overrides in its cache key.
- `google_compute_url_map.cdn`: every path to the backend bucket, so a URL's path is the
  object key.
- `google_compute_ssl_policy.cdn`: TLS 1.2 or later, Google's `MODERN` profile.
- `google_certificate_manager_dns_authorization.cdn`, `google_certificate_manager_certificate.cdn`,
  `google_certificate_manager_certificate_map.cdn` and
  `google_certificate_manager_certificate_map_entry.cdn`: a Google-managed certificate for
  `domain_name`, authorised by a DNS record, in the map the proxy takes. Labelled with
  `labels`.
- `google_compute_target_https_proxy.cdn`: HTTPS with the map's certificate and the SSL
  policy.
- `google_compute_global_address.cdn` and `google_compute_global_forwarding_rule.cdn`: one
  IPv4 address, HTTPS on port 443 only, global external Application Load Balancer
  (`EXTERNAL_MANAGED`). Labelled with `labels`.
- With `dns_zone` only: `google_dns_record_set.authorization`, the certificate's authorisation
  record, and `google_dns_record_set.host`, `domain_name`'s A record to the address.
- With `grant_cdn_fill` only: `google_storage_bucket_iam_member.cdn_fill`, Storage Object Viewer
  for Cloud CDN's fill account on the committed blobs, under a condition on their prefix.
- `data.google_project.this`: the project number, which names the fill account.

## Invariants

- The backend bucket serves the origin bucket with Cloud CDN on (`tests/security.tftest.hcl`,
  run `the_backend_bucket_serves_the_origin_bucket_through_cloud_cdn`), and every path reaches
  it, so artifactweave needs no `path_prefix` (run `every_path_reaches_the_backend_bucket`).
- HTTPS only, on port 443, through the HTTPS proxy at the module's address
  (`tests/security.tftest.hcl`, run `only_https_is_served`), with TLS 1.2 at least (run
  `tls_1_2_at_least`) and a Google-managed certificate for `domain_name`, authorised by DNS,
  through the module's certificate map (run `the_host_is_served_with_its_managed_certificate`).
- Nothing is granted until `grant_cdn_fill` (`tests/security.tftest.hcl`, run
  `the_fill_account_is_not_granted_until_asked`); then Cloud CDN's fill account may read the
  committed blobs, `<store_prefix>sha256/`, and nothing else (run
  `the_fill_account_reads_the_committed_blobs_only`), and no one else, `allUsers` and
  `allAuthenticatedUsers` included, is granted the bucket (run
  `no_viewer_is_granted_the_bucket`).
- With `dns_zone`, the authorisation record and the host's A record go in that zone
  (`tests/security.tftest.hcl`, run `the_records_go_in_dns_zone`); without it, none is created
  and `dns_records` hands both back (`tests/inputs.tftest.hcl`, run
  `without_dns_zone_the_records_are_handed_back`).
- Labels reach the address, the forwarding rule and every Certificate Manager piece
  (`tests/security.tftest.hcl`, run `labels_reach_every_resource_that_takes_them`).
- The cache key holds `response-content-disposition` and `response-content-type`, so one item
  is never served with another's filename, and no header (`tests/cache.tftest.hcl`, run
  `the_cache_key_holds_the_two_overrides`); a signed response stays fresh for a day by default
  and no error response is cached (run `signed_responses_are_cached_and_errors_are_not`); each
  blob is served uncompressed, so ranged requests keep their offsets (run
  `blobs_are_served_unaltered`).
- `store_prefix` is normalised as `blob.gcs.prefix` is (`tests/inputs.tftest.hcl`, run
  `store_prefix_is_normalised_as_the_backend_does`).

## Accepted findings

None: this module carries no `#trivy:ignore`, and trivy reports nothing on it.

## Usage

```hcl
module "cdn" {
  source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/gcp/cdn?ref=v0.1.0" # x-release-please-version

  name           = "artifactweave-example"
  origin_bucket  = module.storage.bucket
  domain_name    = "cdn.example.com"
  dns_zone       = "example-com"
  grant_cdn_fill = true # once the signed URL key is added
}
```

Add the key after the first apply, then apply again with `grant_cdn_fill`:

```bash
gcloud compute backend-buckets add-signed-url-key "$(terraform output -raw backend_bucket)" \
  --key-name artifactweave --key-file cloud-cdn-key
```

## Reference

Generated by terraform-docs (`make deploy-docs`).

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.9 |
| google | ~> 8.6 |

### Providers

| Name | Version |
| ---- | ------- |
| google | 8.6.0 |

### Modules

No modules.

### Resources

| Name | Type |
| ---- | ---- |
| [google_certificate_manager_certificate.cdn](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/certificate_manager_certificate) | resource |
| [google_certificate_manager_certificate_map.cdn](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/certificate_manager_certificate_map) | resource |
| [google_certificate_manager_certificate_map_entry.cdn](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/certificate_manager_certificate_map_entry) | resource |
| [google_certificate_manager_dns_authorization.cdn](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/certificate_manager_dns_authorization) | resource |
| [google_compute_backend_bucket.cdn](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/compute_backend_bucket) | resource |
| [google_compute_global_address.cdn](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/compute_global_address) | resource |
| [google_compute_global_forwarding_rule.cdn](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/compute_global_forwarding_rule) | resource |
| [google_compute_ssl_policy.cdn](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/compute_ssl_policy) | resource |
| [google_compute_target_https_proxy.cdn](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/compute_target_https_proxy) | resource |
| [google_compute_url_map.cdn](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/compute_url_map) | resource |
| [google_dns_record_set.authorization](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/dns_record_set) | resource |
| [google_dns_record_set.host](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/dns_record_set) | resource |
| [google_storage_bucket_iam_member.cdn_fill](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/storage_bucket_iam_member) | resource |
| [google_project.this](https://registry.terraform.io/providers/hashicorp/google/latest/docs/data-sources/project) | data source |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| dns\_zone | Name of the Cloud DNS managed zone, in the provider's project, that holds domain\_name: the certificate's authorisation record and the host's A record go there. Null creates no records; add the two in dns\_records wherever the domain is served. | `string` | `null` | no |
| domain\_name | The CDN's host, such as cdn.example.com, served with a Google-managed certificate. Make it a sibling of artifactweave's host, never a subdomain of it, so clients drop their credentials on the redirect; artifactweave refuses to start otherwise. | `string` | n/a | yes |
| grant\_cdn\_fill | Let Cloud CDN's fill account, service-<project number>@cloud-cdn-fill.iam.gserviceaccount.com, read the committed blobs. Google creates that account when a project's first signed URL key is added, and the grant fails before then: apply, add the key with gcloud, then turn this on. | `bool` | `false` | no |
| labels | Labels for the address, the forwarding rule and the Certificate Manager pieces, the resources here that take them. | `map(string)` | `{}` | no |
| name | Name of the backend bucket, URL map, SSL policy, target proxy, address, forwarding rule and the Certificate Manager pieces, which each kind keeps apart: 1 to 63 lowercase letters, digits and hyphens, starting with a letter. | `string` | n/a | yes |
| origin\_bucket | Name of the bucket Cloud CDN serves, modules/gcp/storage's bucket output. The backend bucket serves its root, so artifactweave's blob.gcs.cdn.cloud\_cdn.path\_prefix stays empty. | `string` | n/a | yes |
| signed\_url\_cache\_max\_age\_sec | How long, in seconds, Cloud CDN keeps a response to a signed URL fresh, whatever the bucket's headers say. Blobs never change, and the signature is checked on every request, cached or not. | `number` | `86400` | no |
| store\_prefix | Object prefix artifactweave writes under (blob.gcs.prefix), as given to modules/gcp/storage. Cloud CDN's fill account may read only the committed blobs under it, <store\_prefix>sha256/. | `string` | `""` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| backend\_bucket | The backend bucket's name, which gcloud compute backend-buckets add-signed-url-key takes. |
| cdn\_fill\_member | The IAM member grant\_cdn\_fill has given read access to the committed blobs, Cloud CDN's fill account; null until it is on. |
| cdn\_fill\_service\_account | Cloud CDN's fill account, which grant\_cdn\_fill lets read the committed blobs; Google creates it with the project's first signed URL key. |
| dns\_records | The records domain\_name needs, as name, type and data: the certificate's DNS authorisation and the host's A record. With dns\_zone they are created there; without it, add them wherever the domain is served. |
| domain | The host artifactweave signs URLs for (blob.gcs.cdn.cloud\_cdn.domain), domain\_name. |
| ip\_address | The load balancer's address, which domain\_name's A record names. |
<!-- END_TF_DOCS -->
