# modules/aws/cdn

A CloudFront distribution in front of one [`modules/aws/storage`](../storage/README.md)
bucket, serving artifactweave's blobs only to URLs artifactweave signs (decision
[0029](../../../../../../docs/research/decisions/0029-cdn-delivery-and-download-redirect.md)).
CloudFront checks a canned-policy signature from a trusted key group on every request and
reads the private bucket through origin access control. [`aws/cdn`](../../../aws/cdn/README.md)
composes it with the storage module. CloudFront is global and the module holds no provider
block: the one regional resource, the certificate for a host of your own, names `us-east-1`
itself, the per-resource region of the AWS provider 6, so no provider alias is needed
(decision [0030](../../../../../../docs/research/decisions/0030-reference-deployments.md)).

S3 allows one policy per bucket, so this module does not write the bucket's policy. It
outputs the statement that lets the distribution read the blobs, `bucket_policy_statement`,
for the caller to pass to the storage module's `policy_statements`.

## What it creates

- `aws_cloudfront_origin_access_control.blobs`: CloudFront signs every request to the bucket
  with SigV4, so the bucket stays private.
- `aws_cloudfront_cache_policy.blobs`: the cache key is the path plus
  `response-content-disposition` and `response-content-type`, the overrides artifactweave signs
  into each URL. CloudFront forwards what the key holds to the origin, so no origin request
  policy is needed, and it strips its own signature parameters before the origin.
- `aws_cloudfront_public_key.signers`: one per entry of `public_keys`, named
  `<name>-<key name>-<random>`, so a replaced key is created before the old one goes.
- `aws_cloudfront_key_group.signers`: the keys the distribution trusts.
- `aws_cloudfront_distribution.cdn`: one behaviour, GET and HEAD over HTTPS for signed URLs
  only, uncompressed, with the bucket's root as its origin. Tagged with `tags`.
- With `domain_name` only: `aws_acm_certificate.cdn` in `us-east-1`, tagged with `tags`, its
  DNS validation record `aws_route53_record.validation` and
  `aws_acm_certificate_validation.cdn`, and `aws_route53_record.alias`, A and AAAA alias
  records to the distribution, all in `zone_id`.

## Invariants

- The bucket is read through origin access control, signing every request with SigV4, at the
  bucket's regional endpoint (`tests/security.tftest.hcl`, run
  `the_bucket_is_read_through_origin_access_control`).
- The origin is the bucket's root, so a URL's path is the object key and artifactweave needs no
  `path_prefix` (`tests/security.tftest.hcl`, run `the_origin_path_is_empty`).
- Only URLs signed by a key in the module's key group are served, by the distribution's only
  behaviour (`tests/security.tftest.hcl`, run `only_signed_urls_are_served`), and the key group
  holds every key in `public_keys` (run `the_key_group_trusts_every_key_given`).
- Viewers use HTTPS, for GET and HEAD only (`tests/security.tftest.hcl`, run
  `viewers_use_https_for_get_and_head_only`).
- CloudFront may read the committed blobs, `<store_prefix>sha256/`, and nothing else in the
  bucket, for this distribution only (`tests/security.tftest.hcl`, run
  `cloudfront_reads_the_committed_blobs_only`).
- A host of your own is served with its validated certificate, by SNI, with TLS 1.2 at least
  (`tests/security.tftest.hcl`, run `a_custom_domain_gets_tls_1_2`).
- Tags reach the distribution and the certificate, the resources that take them
  (`tests/security.tftest.hcl`, run `tags_reach_the_distribution_and_the_certificate`).
- The cache key holds the two response overrides and no other query string, so the signature
  never fragments the cache and one item is never served with another's filename
  (`tests/cache.tftest.hcl`, run `the_cache_key_is_the_path_and_the_two_overrides`); no
  header, cookie or encoding enters it (run `no_header_cookie_or_encoding_enters_the_cache_key`).
- The distribution caches by that policy and serves each blob uncompressed, so ranged requests
  keep their offsets (`tests/cache.tftest.hcl`, run
  `the_distribution_uses_the_cache_policy_and_serves_blobs_unaltered`); an edge copy lives a
  day by default and a year at most (run `edge_copies_last_a_day_by_default`).
- Without `domain_name`, the distribution's `*.cloudfront.net` host serves, with no certificate
  or records (`tests/inputs.tftest.hcl`, run `without_a_domain_the_distribution_host_serves`);
  with it, a DNS-validated certificate in `us-east-1` and A and AAAA alias records in `zone_id`
  (run `a_domain_gets_a_certificate_in_us_east_1_and_alias_records`).
- Every CloudFront name starts with `name` (`tests/inputs.tftest.hcl`, run
  `names_start_with_name`).
- `store_prefix` is normalised as the backend does (`tests/inputs.tftest.hcl`, run
  `store_prefix_is_normalised_as_the_backend_does`).
- `public_keys` holds one to five PEM public keys and never a private key, and `domain_name`
  and `zone_id` are set together (`tests/inputs.tftest.hcl`, runs `at_least_one_public_key`,
  `at_most_five_public_keys`, `a_private_key_is_refused`, `domain_name_needs_zone_id` and
  `zone_id_needs_domain_name`).

## Accepted findings

| ID | Resource | Why |
|---|---|---|
| AWS-0010 | `aws_cloudfront_distribution.cdn` | No access logs: standard logging needs a bucket with ACLs on, which the storage baseline turns off, and artifactweave counts every download it redirects here. |
| AWS-0011 | `aws_cloudfront_distribution.cdn` | No WAF: CloudFront refuses every request without a valid signature before it reaches the bucket, and a web ACL is billed monthly. |

## Usage

Scenarios in this repository use the relative source, `../../modules/aws/cdn`; anyone else can
use the git source, which names the current release. Pass the storage module's `bucket`, `arn`
and `region` outputs as `origin_bucket`, one by one rather than the module itself, whose bucket
policy waits for this module's statement; then pass the statement back to the storage module:

```hcl
module "cdn" {
  source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/aws/cdn?ref=v0.1.0" # x-release-please-version

  name          = "artifactweave-example"
  origin_bucket = { bucket = module.storage.bucket, arn = module.storage.arn, region = module.storage.region }
  public_keys   = { artifactweave = file("cloudfront-public-key.pem") }
}

# In module "storage":
#   policy_statements = [module.cdn.bucket_policy_statement]
```

## Reference

Generated by terraform-docs (`make deploy-docs`).

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.9 |
| aws | ~> 6.67 |

### Providers

| Name | Version |
| ---- | ------- |
| aws | 6.67.0 |

### Modules

No modules.

### Resources

| Name | Type |
| ---- | ---- |
| [aws_acm_certificate.cdn](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/acm_certificate) | resource |
| [aws_acm_certificate_validation.cdn](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/acm_certificate_validation) | resource |
| [aws_cloudfront_cache_policy.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudfront_cache_policy) | resource |
| [aws_cloudfront_distribution.cdn](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudfront_distribution) | resource |
| [aws_cloudfront_key_group.signers](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudfront_key_group) | resource |
| [aws_cloudfront_origin_access_control.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudfront_origin_access_control) | resource |
| [aws_cloudfront_public_key.signers](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/cloudfront_public_key) | resource |
| [aws_route53_record.alias](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route53_record) | resource |
| [aws_route53_record.validation](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route53_record) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| domain\_name | A host of your own for the distribution, such as cdn.example.com, served with an ACM certificate in us-east-1 validated through zone\_id. Make it a sibling of artifactweave's host, never a subdomain of it, so clients drop their credentials on the redirect; artifactweave refuses to start otherwise. Null serves from the distribution's *.cloudfront.net host. | `string` | `null` | no |
| name | Name of the origin access control, the cache policy and the key group, and the start of each public key's: 1 to 64 letters, digits, hyphens and underscores, unique among the account's CloudFront resources of each kind. | `string` | n/a | yes |
| origin\_bucket | The bucket CloudFront reads from, from modules/aws/storage's outputs of the same names. Pass them one by one rather than the module itself, whose bucket policy waits for this module's statement. The origin is the bucket's root, so artifactweave's blob.s3.cdn.cloudfront.path\_prefix stays empty. | ```object({ bucket = string arn = string region = string })``` | n/a | yes |
| price\_class | Which edge locations serve the blobs: PriceClass\_All, PriceClass\_200, or PriceClass\_100 (North America, Europe and Israel). Viewers elsewhere are served from the nearest included location. | `string` | `"PriceClass_All"` | no |
| public\_keys | PEM public keys (RSA 2048 or ECDSA P-256) whose private halves sign artifactweave's URLs, by a static name. Every one is trusted: to rotate, add the new key, point artifactweave at it, then remove the old one once the URLs it signed have expired. Terraform state holds these values, so never pass a private key. | `map(string)` | n/a | yes |
| store\_prefix | Key prefix artifactweave writes under (blob.s3.prefix), as given to modules/aws/storage. CloudFront may read only the committed blobs under it, <store\_prefix>sha256/. | `string` | `""` | no |
| tags | Tags for the distribution and the certificate, the resources here that take them. | `map(string)` | `{}` | no |
| zone\_id | ID of the Route 53 public hosted zone that holds domain\_name: the certificate's validation record and the host's A and AAAA alias records go there. Set it with domain\_name, and only then. | `string` | `null` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| bucket\_policy\_statement | The bucket policy statement that lets this distribution read the committed blobs: pass it to modules/aws/storage's policy\_statements. |
| distribution\_arn | The distribution's ARN, which the bucket policy statement names as the only source CloudFront may read for. |
| distribution\_domain | The distribution's own *.cloudfront.net host, which domain\_name's alias records point at. |
| distribution\_id | The distribution's ID, for the CloudFront console and CLI. |
| domain | The host artifactweave signs URLs for (blob.s3.cdn.cloudfront.domain): domain\_name when given, otherwise the distribution's *.cloudfront.net host. |
| key\_pair\_ids | Each public key's ID by its name in public\_keys: blob.s3.cdn.cloudfront.key\_pair\_id for the key whose private half artifactweave signs with. |
<!-- END_TF_DOCS -->
