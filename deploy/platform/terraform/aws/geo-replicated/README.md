# aws/geo-replicated

One S3 store that S3 itself copies to a second region: a primary bucket in `eu-west-2`
and a replica in `eu-west-1`, joined by S3 cross-region replication (decision
[0030](../../../../../docs/research/decisions/0030-reference-deployments.md)). It is the
shape for operators who want the cloud's own replication rather than artifactweave's
mirror. Apply [`aws/bootstrap`](../bootstrap/README.md) first: it holds GitHub's trust,
the roles and the permissions boundary.

## How it relates to artifactweave

artifactweave sees one store, the primary bucket. It writes there, reads there and signs
download URLs there. The replica is S3's copy:

- **Invisible to artifactweave.** The mirror's replica index never records it, and it never
  counts toward a mirror's write quorum (decision
  [0028](../../../../../docs/research/decisions/0028-blob-mirroring.md)). Don't make the
  replica a mirror member as well: you'd pay for two copies of every write and the mirror
  would race S3 for the same keys.
- **Asynchronous.** Without Replication Time Control, S3 gives no time bound. With it
  (`replication_time_control = true`), 99.9% of objects replicate within 15 minutes
  ([research 20](../../../../../docs/research/20-reference-deployments.md#21-aws)).
- **Committed blobs only.** The rule replicates keys under `<store_prefix>sha256/`. Upload
  parts and conformance prefixes stay in the primary, where the lifecycle rules remove them.
- **Deletes don't replicate.** A blob artifactweave's garbage collection removes from the
  primary stays in the replica, so a failover never misses a blob that a lagging delete
  would have taken. The replica's lifecycle rules expire noncurrent versions only, so
  replicated blobs accumulate there until an operator removes them.

## Failover

A presigned URL names one bucket, so URLs artifactweave issued for the primary keep
pointing at it. To fail over:

1. Re-point artifactweave at the replica with the `failover_artifactweave_config` output
   (bucket, region and prefix), and restart it.
2. Expect blobs written in the last minutes before the outage to be missing from the
   replica. Their downloads 404 until the primary is back, because replication is
   asynchronous.
3. After the primary recovers, copy anything written to the replica during the failover
   back before re-pointing. S3 replication is one-way here.

## What it creates

- **Two private buckets**, `artifactweave-geo-<suffix>` and `…-r`. Each has public access
  blocked, ACLs disabled, SSE-S3 and a TLS-only policy, with versioning on (replication
  needs it). Destroy removes every object version with them.
- **Lifecycle rules on both buckets:**
  - `local/_uploads/` parts expire after 2 days, and the primary's `conformance/` after a
    day;
  - incomplete multipart uploads are aborted after a day;
  - noncurrent versions expire after a day.
- **Bucket policies** granting the app role both buckets' objects: the bootstrap's
  `artifactweave-conformance`, unless `app_role_arn` names another.
- **The replication role**, under `/artifactweave-scenarios/` and carrying the bootstrap's
  boundary, which is the only kind of role the deployer may create. It can read this
  primary's versions and write to this replica only, and its trust names this source bucket
  and account.
- **The replication rule:** committed blobs, delete markers not replicated, the `STANDARD`
  storage class, and RTC with metrics when enabled.

## Use

`aws login` first. From this directory:

```bash
make plan
make apply
terraform output -raw artifactweave_config   # the blob section for artifactweave.yaml
make destroy
```

There is no local container or conformance target; `aws/single-bucket` has those. Every
target that touches AWS first checks that the CLI is signed in to the account holding the
delegated zone.

## Scenario runs

`platform-scenarios.yml` runs this root as the deployer
(`-f cloud=aws -f scenario=geo-replicated`), through
[`../../scenario-ci.sh`](../../scenario-ci.sh) and [`ci-vars.sh`](ci-vars.sh). The buckets
are named `artifactweave-awscn-<instance>-<suffix>` and `…-r`, granted to the deployer, and
tagged for reap. Reap finds them in either region.

Besides the generic checks, [`test/platform`](../../../../../test/platform/replica_test.go)
waits up to 12 minutes for S3 to mark the blob's replication `COMPLETED`, then reads the
replica copy in `eu-west-1`. A replication still pending after that is reported, not
failed, because S3 promises no bound without RTC. A `FAILED` replication fails the run.

## Cost

Cents per run:
- storage for 40 MiB in two regions for minutes;
- S3's inter-region data transfer for the replicated bytes, charged per GB;
- requests.

Replication Time Control adds a per-GB charge, and is off by default. Destroy the scenario
when the run is done.

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
| aws.replica | 6.67.0 |
| random | 3.9.1 |

### Resources

| Name | Type |
| ---- | ---- |
| [aws_iam_role.replication](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) | resource |
| [aws_iam_role_policy.replication](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy) | resource |
| [aws_s3_bucket.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket) | resource |
| [aws_s3_bucket.replica](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket) | resource |
| [aws_s3_bucket_lifecycle_configuration.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_lifecycle_configuration) | resource |
| [aws_s3_bucket_lifecycle_configuration.replica](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_lifecycle_configuration) | resource |
| [aws_s3_bucket_ownership_controls.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_ownership_controls) | resource |
| [aws_s3_bucket_ownership_controls.replica](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_ownership_controls) | resource |
| [aws_s3_bucket_policy.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_policy) | resource |
| [aws_s3_bucket_policy.replica](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_policy) | resource |
| [aws_s3_bucket_public_access_block.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_public_access_block) | resource |
| [aws_s3_bucket_public_access_block.replica](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_public_access_block) | resource |
| [aws_s3_bucket_replication_configuration.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_replication_configuration) | resource |
| [aws_s3_bucket_server_side_encryption_configuration.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_server_side_encryption_configuration) | resource |
| [aws_s3_bucket_server_side_encryption_configuration.replica](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_server_side_encryption_configuration) | resource |
| [aws_s3_bucket_versioning.blobs](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_versioning) | resource |
| [aws_s3_bucket_versioning.replica](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_versioning) | resource |
| [random_id.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/id) | resource |
| [aws_caller_identity.current](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/caller_identity) | data source |
| [aws_iam_policy_document.bucket](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.replication](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.replication_trust](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_role.conformance](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_role) | data source |
| [aws_partition.current](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/partition) | data source |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_role\_arn | ARN of the role artifactweave runs as, which the bucket policies grant. Empty looks up conformance\_role\_name; scenario runs pass the deployer. | `string` | `""` | no |
| boundary\_policy\_name | Name of aws/bootstrap's permissions boundary (under /artifactweave/), which every role a scenario creates must carry. | `string` | `"artifactweave-scenario-boundary"` | no |
| conformance\_role\_name | Name of the data-plane role aws/bootstrap created; the bucket policies grant it both buckets' objects. | `string` | `"artifactweave-conformance"` | no |
| name | Prefix for the bucket names. It must start with artifactweave-, the only buckets the deployer may manage, and leave room for the suffixes within S3's 63 characters; scenario runs pass artifactweave-awscn-<instance>. | `string` | `"artifactweave-geo"` | no |
| region | Region of the primary bucket, which artifactweave writes to (the bootstrap's region). | `string` | `"eu-west-2"` | no |
| replica\_region | Region of the replica bucket S3 replicates into. | `string` | `"eu-west-1"` | no |
| replication\_time\_control | S3 Replication Time Control: 99.9% of objects replicated within 15 minutes, with replication metrics, billed per GB replicated. Off by default. | `bool` | `false` | no |
| scenario\_labels | Tags for the buckets and the replication role. Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches. | `map(string)` | `{}` | no |
| scenario\_role\_path | IAM path for the replication role: aws/bootstrap's scenario path, the only one the deployer may create roles under. | `string` | `"/artifactweave-scenarios/"` | no |
| store\_prefix | Key prefix artifactweave writes under (blob.s3.prefix). Keys under <store\_prefix>sha256/ replicate. | `string` | `"local/"` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| artifactweave\_config | The blob section of artifactweave.yaml: one store, the primary bucket. Credentials come from the AWS default chain. |
| bucket | Primary bucket for blob.s3.bucket. |
| expires\_at | The expires-at tag (epoch seconds) a scenario run set, read by reap; empty outside scenario runs. |
| failover\_artifactweave\_config | The same blob section pointed at the replica, for a failover: re-point artifactweave here and restart it. |
| region | Region of the primary bucket (blob.s3.region). |
| replica\_bucket | Replica bucket S3 replicates committed blobs into. |
| replica\_region | Region of the replica bucket. |
| replicated\_prefix | Keys under this prefix (the committed blobs) replicate. |
| storage\_host | Host a downloadRedirect repository's signed URLs point at: the primary bucket's virtual-hosted endpoint. |
| store\_prefix | Key prefix artifactweave writes under (blob.s3.prefix). |
<!-- END_TF_DOCS -->
