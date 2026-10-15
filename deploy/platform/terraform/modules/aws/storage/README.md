# modules/aws/storage

One private S3 bucket a scenario stores blobs in, whose bucket policy grants the role
artifactweave runs as its objects. `aws/multi-region` calls it once per region. The module
holds no provider block: every resource names its `region`, the per-resource region of the
AWS provider 6, so one provider configuration serves buckets in several regions (decision
[0030](../../../../../../docs/research/decisions/0030-reference-deployments.md)).

The bucket blocks all public access, disables ACLs, encrypts with S3-managed keys, refuses
requests without TLS, deletes abandoned `<store_prefix>_uploads/` parts after two days and
aborts multipart uploads after one. `tests/security.tftest.hcl` checks those invariants on a
mocked plan, so the scenarios that call the module check only how they compose it.

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.6 |
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

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_role\_arn | ARN of the role artifactweave runs as, which the bucket policy grants this bucket's objects. | `string` | n/a | yes |
| name | Bucket name: 3 to 63 lowercase letters, digits and hyphens, globally unique. The deployer may only manage buckets starting artifactweave-. | `string` | n/a | yes |
| region | Region of the bucket, for example eu-west-2. | `string` | n/a | yes |
| store\_prefix | Key prefix artifactweave writes under (blob.s3.prefix); its \_uploads/ parts expire after two days. | `string` | n/a | yes |
| tags | Tags for the bucket. Scenario runs set artifactweave-scenario, instance and expires-at, which reap matches. | `map(string)` | `{}` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| bucket | The bucket's name, for blob.s3.bucket. |
| host | The bucket's virtual-hosted endpoint, where the SDK's presigned URLs point. |
| signed\_url\_prefix | What a presigned URL for an object in this bucket starts with after the scheme. |
<!-- END_TF_DOCS -->
