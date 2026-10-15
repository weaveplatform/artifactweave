# aws/single-bucket

An S3 acceptance bucket for artifactweave's S3 backend in the lab account, `eu-west-2`
(decision [0030](../../../../../docs/research/decisions/0030-reference-deployments.md)). The
real-cloud conformance tests (`cloud-conformance.yml`, job `s3`) and a local run both
use it, under separate prefixes. GitHub's OIDC provider and the conformance role live in
[`deploy/ci/terraform/aws`](../../../../ci/terraform/aws/README.md); apply that first.

## What it creates

- A private bucket `artifactweave-acc-<suffix>`: public access blocked, ACLs disabled
  (bucket owner enforced), SSE-S3, TLS only. Destroy removes its objects with it.
- Lifecycle rules: `local/_uploads/` parts expire after 2 days, `conformance/` after a day,
  and incomplete multipart uploads are aborted after a day, because S3 bills them until
  aborted.
- A bucket policy granting the app role this bucket's objects and listings: the bootstrap's
  `artifactweave-conformance` role, unless `app_role_arn` names another. The role has no
  policy of its own, so its rights end with the bucket and leave with it.
- The outputs a scenario run reads: `artifactweave_config` (the server's blob configuration
  for this bucket), `storage_host` (the bucket's virtual-hosted endpoint, where presigned
  URLs point) and `expires_at`.

## Use

`aws login` first. From this directory (or `make aws-<target>` from the repository root):

```bash
make plan
make apply
make github-vars     # sets CONFORMANCE_S3_BUCKET for cloud-conformance.yml
make conformance     # the contract and probes, as your own login
make up              # artifactweave on http://127.0.0.1:8083 against the bucket
make smoke           # 40 MiB through it, and a downloadRedirect 302 to the bucket
make verify          # test/platform against the bucket, as CI runs it
make down            # stops it; make clean also drops .local/
make destroy         # removes the bucket and its objects
```

The local run gets short-lived credentials exported from your `aws login` session
(`aws configure export-credentials`, into `.local/aws.env`); run `make down up` once they
expire. Every target
that touches AWS first checks that the CLI is signed in to the account holding the
delegated zone.

## Scenario runs

`platform-scenarios.yml` can drive this root as the deployer (`-f cloud=aws`), through
[`deploy/ci/scenario-ci.sh`](../../../../ci/scenario-ci.sh) and [`ci-vars.sh`](ci-vars.sh):
the bucket is named `artifactweave-awscn-<instance>-<suffix>` (inside the `artifactweave-`
prefix the deployer may manage), the bucket policy grants the deployer, which runs
artifactweave in the same job, and the bucket is tagged with the scenario, instance and
`expires-at` that reap matches. The pull request proof runs it alongside GCP and Azure.

## Cost

A few cents per conformance run (requests and kilobytes of storage, deleted when the run
ends). Destroy the scenario when the run is done.

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.6 |
| aws | ~> 6.67 |
| random | ~> 3.9 |

### Providers

| Name | Version |
| ---- | ------- |
| aws | 6.67.0 |
| random | 3.9.1 |

### Resources

| Name | Type |
| ---- | ---- |
| [aws_s3_bucket.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket) | resource |
| [aws_s3_bucket_lifecycle_configuration.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_lifecycle_configuration) | resource |
| [aws_s3_bucket_ownership_controls.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_ownership_controls) | resource |
| [aws_s3_bucket_policy.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_policy) | resource |
| [aws_s3_bucket_public_access_block.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_public_access_block) | resource |
| [aws_s3_bucket_server_side_encryption_configuration.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_server_side_encryption_configuration) | resource |
| [random_id.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/id) | resource |
| [aws_iam_role.conformance](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_role) | data source |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_role\_arn | ARN of the role artifactweave runs as, which the bucket policy grants. Empty looks up conformance\_role\_name; scenario runs pass the deployer, which runs artifactweave in the same job and cannot read the bootstrap's roles. | `string` | `""` | no |
| conformance\_role\_name | Name of the data-plane role aws/bootstrap created; the bucket policy grants it this bucket's objects. | `string` | `"artifactweave-conformance"` | no |
| name | Prefix for the bucket name. It must start with artifactweave-, the only buckets the deployer may manage; scenario runs pass artifactweave-awscn-<instance>. | `string` | `"artifactweave-acc"` | no |
| region | Region of the bucket (the bootstrap's region). | `string` | `"eu-west-2"` | no |
| scenario\_labels | Tags for the bucket. Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches. | `map(string)` | `{}` | no |
| store\_prefix | Key prefix the local run writes under (blob.s3.prefix). | `string` | `"local/"` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| app\_role\_arn | The role the bucket policy grants: the bootstrap's conformance role, unless app\_role\_arn names another. |
| artifactweave\_config | The blob section of artifactweave.yaml for this bucket; credentials come from the AWS default chain. |
| bucket | Bucket for blob.s3.bucket and ARTIFACTWEAVE\_TEST\_S3\_BUCKET (CONFORMANCE\_S3\_BUCKET). |
| expires\_at | The expires-at tag (epoch seconds) a scenario run set, read by reap; empty outside scenario runs. |
| region | Region for blob.s3.region and ARTIFACTWEAVE\_TEST\_S3\_REGION. |
| storage\_host | Host a downloadRedirect repository's signed URLs point at: the bucket's virtual-hosted endpoint, which the SDK presigns. |
| store\_prefix | Key prefix the local run writes under (blob.s3.prefix). |
<!-- END_TF_DOCS -->
