# modules/gcp/storage

One private GCS bucket a scenario stores blobs in, with `roles/storage.objectAdmin` on it
for the service account artifactweave runs as. `gcp/multi-region` calls it once per region.
The module holds no provider block, so the caller's `google` provider and project apply
(decision [0030](../../../../../../docs/research/decisions/0030-reference-deployments.md)).

The bucket is a single region with uniform access and public access prevention, soft delete
is off (the backend composes and deletes temporary parts, which soft delete would bill), and
a lifecycle rule deletes abandoned `<store_prefix>_uploads/` parts after two days.
`tests/security.tftest.hcl` checks those invariants on a mocked plan, so the scenarios that
call the module check only how they compose it.

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.6 |
| google | ~> 8.6 |

### Providers

| Name | Version |
| ---- | ------- |
| google | 8.6.0 |

### Resources

| Name | Type |
| ---- | ---- |
| [google_storage_bucket.blobs](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/storage_bucket) | resource |
| [google_storage_bucket_iam_member.app](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/storage_bucket_iam_member) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_service\_account | Email of the service account artifactweave runs as; it gets object rights on this bucket only. | `string` | n/a | yes |
| labels | Labels for the bucket. Scenario runs set artifactweave-scenario, instance and expires-at, which reap matches. | `map(string)` | `{}` | no |
| location | Region of the bucket, for example europe-west2. A single region: the mirror, not the bucket, keeps the second copy. | `string` | n/a | yes |
| name | Bucket name, globally unique. | `string` | n/a | yes |
| store\_prefix | Object prefix artifactweave writes under (blob.gcs.prefix); its \_uploads/ parts expire after two days. | `string` | n/a | yes |

### Outputs

| Name | Description |
| ---- | ----------- |
| bucket | The bucket's name, for blob.gcs.bucket. |
| signed\_url\_prefix | What a signed URL for an object in this bucket starts with after the scheme: path-style, as the backend signs them. |
<!-- END_TF_DOCS -->
