# modules/aws/storage

One private S3 bucket and its bucket policy, granting the IAM roles in `principals` the
bucket's objects. [`aws/single-bucket`](../../../aws/single-bucket/README.md) calls it once,
[`aws/multi-region`](../../../aws/multi-region/README.md) once per region,
[`aws/geo-replicated`](../../../aws/geo-replicated/README.md) twice, versioned, for
[`modules/aws/replication`](../replication/README.md) to join,
[`aws/cdn`](../../../aws/cdn/README.md) once, with [`modules/aws/cdn`](../cdn/README.md)'s read
grant in `policy_statements`, and [`multicloud/mirror`](../../../multicloud/mirror/README.md)
once, for its AWS mirror member. The module holds no provider block: every resource names its
`region`, the per-resource region of the AWS provider 6, so one provider configuration serves
buckets in several regions without aliases (decision [0030](../../../../../../docs/research/decisions/0030-reference-deployments.md)).

## What it creates

- `aws_s3_bucket.blobs`: the bucket, tagged with `tags`; `force_destroy` lets a destroy empty
  it.
- `aws_s3_bucket_ownership_controls.blobs`: bucket owner enforced, which turns ACLs off.
- `aws_s3_bucket_public_access_block.blobs`: every form of public access blocked.
- `aws_s3_bucket_server_side_encryption_configuration.blobs`: S3-managed encryption (SSE-S3)
  by default.
- `aws_s3_bucket_versioning.blobs`: only when `versioning` is true, as S3 replication needs.
- `aws_s3_bucket_lifecycle_configuration.blobs`: the expiry backstops below.
- `aws_s3_bucket_policy.blobs`: TLS required, and object and listing rights for `principals`,
  followed by any `policy_statements`, such as a CDN's read grant from
  [`modules/aws/cdn`](../cdn/README.md): S3 allows one policy per bucket.

## Invariants

- No public access: all four public access blocks are on (`tests/security.tftest.hcl`, run
  `public_access_is_blocked`).
- Access is identity-based: ACLs are disabled (`tests/security.tftest.hcl`, run
  `acls_are_disabled`).
- TLS only: the bucket policy denies every request without it (`tests/security.tftest.hcl`,
  run `plain_http_is_refused`).
- Objects are encrypted at rest with S3-managed keys (`tests/security.tftest.hcl`, run
  `objects_are_encrypted`).
- Every resource is managed in `region`, not the provider's (`tests/security.tftest.hcl`, run
  `every_resource_is_in_the_region_given`).
- The principals reach this bucket and its objects only (`tests/security.tftest.hcl`, run
  `principals_reach_this_bucket_only`), with object and listing actions and no bucket
  administration (run `principals_get_object_and_listing_rights_only`).
- A caller's `policy_statements` follow the module's own, never replacing them
  (`tests/security.tftest.hcl`, run `policy_statements_follow_the_modules_own`), and each
  has a Sid of its own (`tests/inputs.tftest.hcl`, runs `policy_statements_need_their_own_sid`
  and `policy_statements_need_a_sid`). The public access block still refuses one that would
  make the bucket public.
- Tags reach the bucket, the one resource that takes them (`tests/security.tftest.hcl`, run
  `tags_reach_the_bucket`).
- `<store_prefix>_uploads/` expires after 2 days (`tests/lifecycle.tftest.hcl`, run
  `abandoned_upload_parts_expire_after_two_days`).
- Incomplete multipart uploads abort after 1 day (`tests/lifecycle.tftest.hcl`, run
  `multipart_uploads_abort_after_a_day`).
- Each `expiring_prefixes` entry expires after its days (`tests/lifecycle.tftest.hcl`, run
  `expiring_prefixes_expire_after_their_days`).
- Versioning is on only when asked (`tests/lifecycle.tftest.hcl`, runs
  `an_unversioned_bucket_keeps_the_two_backstops` and `versioning_turns_on_when_asked`), and
  then noncurrent versions expire after 1 day (run
  `noncurrent_versions_expire_after_a_day_when_versioned`).
- `store_prefix` is normalised as the backend does: no leading slash, one trailing slash
  (`tests/inputs.tftest.hcl`, runs `store_prefix_gains_a_trailing_slash` and
  `store_prefix_loses_a_leading_slash`).
- A destroy keeps a bucket that holds objects unless `force_destroy` is true
  (`tests/inputs.tftest.hcl`, run `force_destroy_is_off_by_default`).

## Accepted findings

| ID | Resource | Why |
|---|---|---|
| AWS-0089 | `aws_s3_bucket.blobs` | No access logging: CloudTrail records the management calls, and server access logs need a second bucket. |
| AWS-0090 | `aws_s3_bucket.blobs` | Versioning is off unless `versioning` turns it on: blobs are content-addressed and never rewritten, and versioning would keep every deleted upload part; replication is what needs it. |
| AWS-0132 | `aws_s3_bucket_server_side_encryption_configuration.blobs` | S3-managed keys; a KMS key would add a monthly fee and a key policy to manage. |

## Usage

Scenarios in this repository use the relative source, `../../modules/aws/storage`; anyone else
can use the git source, which names the current release.

```hcl
module "storage" {
  source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/aws/storage?ref=v0.1.0" # x-release-please-version

  name       = "artifactweave-example"
  region     = "eu-west-2"
  principals = { app = "arn:aws:iam::111122223333:role/artifactweave" }
}
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
| [aws_s3_bucket.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket) | resource |
| [aws_s3_bucket_lifecycle_configuration.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_lifecycle_configuration) | resource |
| [aws_s3_bucket_ownership_controls.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_ownership_controls) | resource |
| [aws_s3_bucket_policy.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_policy) | resource |
| [aws_s3_bucket_public_access_block.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_public_access_block) | resource |
| [aws_s3_bucket_server_side_encryption_configuration.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_server_side_encryption_configuration) | resource |
| [aws_s3_bucket_versioning.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_versioning) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| expiring\_prefixes | Further key prefixes, from the bucket's root, whose objects expire after the given number of days, for example { "scratch/" = 1 }. | `map(number)` | `{}` | no |
| force\_destroy | Let terraform destroy delete the bucket with every object (and version) still in it. Off, a destroy fails on a bucket that holds anything. | `bool` | `false` | no |
| name | Bucket name: 3 to 63 lowercase letters, digits and hyphens, globally unique. | `string` | n/a | yes |
| policy\_statements | Further bucket policy statements, appended after the module's own, because S3 allows one policy per bucket: a CDN's read grant, for one (modules/aws/cdn's bucket\_policy\_statement). Each needs a Sid other than the module's TLSOnly, AppObjects and AppListing. | `any` | `[]` | no |
| principals | ARNs of the IAM roles that get this bucket's objects and listing through the bucket policy, by a static name (app, for one), so the plan does not wait for the ARNs. | `map(string)` | n/a | yes |
| region | Region of the bucket and of every resource that configures it. | `string` | n/a | yes |
| store\_prefix | Key prefix artifactweave writes under (blob.s3.prefix), normalised as the backend does: no leading slash, one trailing slash. Its \_uploads/ parts expire after two days. | `string` | `""` | no |
| tags | Tags for the bucket, the one resource here that takes them. | `map(string)` | `{}` | no |
| versioning | Turn on object versioning, as S3 replication needs on both buckets. Noncurrent versions then expire after one day, so deleted blobs stop costing. | `bool` | `false` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| arn | The bucket's ARN, for policies and replication rules that name it. |
| bucket | The bucket's name, for blob.s3.bucket. |
| host | The bucket's virtual-hosted endpoint, where the SDK's presigned URLs point. |
| policy | The bucket policy's JSON: the module's own statements, then policy\_statements. |
| region | The bucket's region, for blob.s3.region. |
| signed\_url\_prefix | What a presigned URL for an object in this bucket starts with after the scheme. |
| versioned | Whether versioning is on, which S3 replication requires of both buckets. |
<!-- END_TF_DOCS -->
