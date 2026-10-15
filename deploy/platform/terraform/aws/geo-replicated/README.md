# aws/geo-replicated: one S3 store that S3 copies to a second region

A primary S3 bucket that S3 cross-region replication copies into a replica bucket in a second
region. artifactweave sees one store, the primary; the replica is S3's copy and the failover
target. Choose it to have the cloud replicate on its own, with artifactweave unaware of the
second region. Choose `multi-region` instead for copies that artifactweave writes and reads
itself, so a redirect never lands on a missing copy, or `single-bucket` when one region is
enough.

## What it deploys

- The primary bucket through [`modules/aws/storage`](../../modules/aws/storage/README.md),
  `<name>-<suffix>` in `region`, and the replica, `<name>-<suffix>-r` in `replica_region`.
  Both are versioned, as replication needs, and both bucket policies grant `app_role_arn`
  their objects: the primary's in normal running, the replica's after a failover.
- The replication through [`modules/aws/replication`](../../modules/aws/replication/README.md):
  the IAM role S3 assumes, `<name>-<suffix>-repl`, and a rule on the primary copying the
  committed blobs, the keys under `<store_prefix>sha256/`, into the replica.
- `random_id.suffix`: eight hex characters shared by the names, so the buckets are globally
  unique.

## How it behaves

- **artifactweave sees one store**, the primary. It writes there, reads there and signs
  download URLs there. The replica is invisible to it: it never counts toward a mirror's
  write quorum (decision
  [0028](../../../../../docs/research/decisions/0028-blob-mirroring.md)). Don't make the
  replica a mirror member as well: you'd pay for two copies of every write, and the mirror
  would race S3 for the same keys.
- **Replication is asynchronous.** Without Replication Time Control, S3 gives no time bound.
  With it (`replication_time_control = true`), 99.9% of objects replicate within 15 minutes
  ([research 20](../../../../../docs/research/20-reference-deployments.md#21-aws)).
- **Committed blobs only.** Upload parts stay in the primary, where its lifecycle rules
  remove them.
- **Deletes don't replicate.** A blob artifactweave's garbage collection removes from the
  primary stays in the replica, so a failover never misses a blob that a lagging delete would
  have taken. The replica's lifecycle rules expire noncurrent versions only, so replicated
  blobs accumulate there until an operator removes them.
- **Failing over.** A presigned URL names one bucket, so URLs issued for the primary keep
  pointing at it. To fail over, re-point artifactweave at the replica with the
  `failover_artifactweave_config` output and restart it. Blobs written in the last minutes
  before the outage may be missing from the replica, and their downloads 404 until the
  primary is back. Replication is one-way, so copy anything written to the replica during
  the failover back before re-pointing at the primary.

## Prerequisites

- Terraform 1.9 or later, and AWS credentials the SDK chain finds (`aws login`, a profile, a
  CI role) for the account that will hold the buckets.
- Rights to create S3 buckets, their policies, versioning, encryption, lifecycle, public
  access and replication settings in both regions, and an IAM role with an inline policy.
  Where roles may be created only under a path or with a permissions boundary, pass them as
  `iam_role_path` and `permissions_boundary_arn`.
- The IAM role artifactweave runs as, created elsewhere (an EC2 instance profile's role, an
  EKS Pod Identity role). This scenario grants it the buckets' objects; it does not create
  it.

### Getting the code

Copy this directory together with `modules/aws/` and `.terraform.lock.hcl`, keeping the
`../../modules` layout, or point each module at the repository:

```hcl
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/aws/storage?ref=v0.0.0" # x-release-please-version
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/aws/replication?ref=v0.0.0" # x-release-please-version
```

## Inputs to set

```hcl
# terraform.tfvars
name           = "artifactweave-example"
region         = "eu-west-2"
replica_region = "eu-west-1"
app_role_arn   = "arn:aws:iam::111122223333:role/artifactweave"

# Optional
# store_prefix             = "artifacts/"
# replication_time_control = true
# expiring_prefixes        = { "scratch/" = 1 }
# force_destroy            = true
# tags                     = { team = "platform" }
# permissions_boundary_arn = "arn:aws:iam::111122223333:policy/boundary"
# iam_role_path            = "/artifactweave/"
```

```bash
terraform init
terraform apply
```

## The artifactweave configuration

`artifactweave_config` renders the `blob` section of `artifactweave.yaml`: one `s3` store, the
primary bucket, in its region, with `prefix` only when `store_prefix` is set. The store names
no credentials: the AWS SDK chain supplies the app role's. `failover_artifactweave_config` is
the same section pointed at the replica. The example below is the rendered output for the
example inputs above, with the random suffix `0a1b2c3d`; the `readme_example` test fails if
they differ.

<!-- BEGIN_CONFIG_EXAMPLE -->
```yaml
blob:
  backend: s3
  s3:
    bucket: artifactweave-example-0a1b2c3d
    region: eu-west-2
```
<!-- END_CONFIG_EXAMPLE -->

### Running artifactweave with it

```bash
terraform output -raw artifactweave_config > artifactweave.yaml
export ARTIFACTWEAVE_MASTER_KEY="$(openssl rand -base64 32)"   # keep it: it seals stored secrets
export ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD='choose one'      # read on first start only
artifactweave serve --config artifactweave.yaml
```

Run it as `app_role_arn`, on an instance or pod that has the role, so the AWS SDK chain finds
its credentials. A `downloadRedirect` repository answers downloads with a 302 to a presigned
URL on `storage_host`, the primary's virtual-hosted endpoint. The
[configuration reference](../../../../../docs/operations/configuration-reference.md) lists
every key.

## Security

Both buckets carry the [`modules/aws/storage` invariants](../../modules/aws/storage/README.md#invariants):
public access blocked, ACLs disabled, TLS required, S3-managed encryption, abandoned uploads
expired, and noncurrent versions expired after a day. The app role can read, write, list and
delete objects in the two buckets through their bucket policies, and nothing else. The
replication role carries the
[`modules/aws/replication` invariants](../../modules/aws/replication/README.md#invariants):
only S3 may assume it, only for the primary in this account, and it can read the primary's
versions and write copies into the replica, nothing more.

### Accepted findings

| ID | Resource (module) | Why |
|---|---|---|
| AWS-0089 | `aws_s3_bucket.blobs` (`aws/storage`) | No access logging: CloudTrail records the management calls, and server access logs need another bucket in each region. |
| AWS-0090 | `aws_s3_bucket.blobs` (`aws/storage`) | Accepted in the module for unversioned buckets; both buckets here are versioned, so it does not apply. |
| AWS-0132 | `aws_s3_bucket_server_side_encryption_configuration.blobs` (`aws/storage`) | S3-managed keys; a KMS key in each region would add a monthly fee and a replication key grant. |

## Cost

### Steady state

Storage is billed in both buckets: S3 Standard, roughly $0.023 to $0.024 per GB-month in the
European regions, for every committed blob twice, plus the noncurrent versions deletes leave
for a day. S3 charges inter-region data transfer for the replicated bytes (roughly $0.02 per
GB) and replication requests. Replication Time Control adds a per-GB charge and is off by
default. Downloads leaving AWS are internet egress. There is no fixed or hourly charge.

### Per test run

About one cent: a proof stores well under 100 MiB in two regions for minutes, and its cost is
mostly the test's downloads leaving AWS, the replicated bytes and the requests.

## Teardown

`terraform destroy` removes the replication rule and role, then both buckets. It fails while
a bucket holds any object version unless `force_destroy` is true, which deletes them with the
bucket. Nothing outlives the scenario: the app role is not this scenario's.

## How this scenario is tested

- **Mocked tests** (`tests/scenario.tftest.hcl`, in `make deploy-check`) check the
  composition: `readme_example` renders the example above from the example inputs; `ci_run`
  uses the longest instance a proof produces, checking that the bucket and role names fit
  their limits and the outputs reap and `test/platform` read; others check the replicated
  prefix, the failover configuration and the inputs' validation.
- **The real-cloud proof** (`platform-scenarios.yml`, on a pull request that changes this
  scenario, a module it calls or the scenario driver) applies an instance as the CI deployer,
  which also runs artifactweave, and runs `test/platform`. Besides the generic round trip and
  redirect, [`replica_test.go`](../../../../../test/platform/replica_test.go) waits up to 12
  minutes for S3 to mark the blob's replication `COMPLETED`, then reads the replica's copy. A
  replication still pending after that is reported, not failed, because S3 promises no bound
  without Replication Time Control; a `FAILED` one fails the run. The proof then destroys the
  instance, applies and destroys another from its remote state alone, and checks that reap
  removes an expired one.
- **Harness outputs** (`harness.tf`): `expires_at`, the tag reap reads from the state.
- **Locally**, `make apply`, `up`, `smoke`, `verify` and `destroy` run the same checks
  against an instance in the lab ([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)).

| Variable | Scenario run (`ci-vars.sh`) | Operator run (`Makefile`, `deploy/ci/lab-vars.sh`) |
|---|---|---|
| `name` | `${NAME_PREFIX}${INSTANCE}`: `artifactweave-awscn-<instance>`, inside the bucket names the deployer may manage | `artifactweave-geo` |
| `region` | `PLATFORM_AWS_REGION` | `LAB_AWS_REGION` |
| `replica_region` | `eu-west-1`, the lab's choice | `eu-west-1` |
| `app_role_arn` | `PLATFORM_AWS_ROLE_ARN`, the deployer | the bootstrap's conformance role, in the signed-in account |
| `permissions_boundary_arn` | the bootstrap's scenario boundary, in the deployer's account: the deployer may create roles only with it | none |
| `iam_role_path` | the bootstrap's scenario path, the only one the deployer may create roles under | none |
| `tags` | `$SCENARIO_LABELS`: `artifactweave-scenario`, `instance`, `expires-at` | none |
| `force_destroy` | `true` | `true` |

## Reference

Generated by terraform-docs (`make deploy-docs`).

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.9 |
| aws | ~> 6.67 |
| random | ~> 3.9 |

### Providers

| Name | Version |
| ---- | ------- |
| random | 3.9.1 |

### Modules

| Name | Source | Version |
| ---- | ------ | ------- |
| primary | ../../modules/aws/storage | n/a |
| replica | ../../modules/aws/storage | n/a |
| replication | ../../modules/aws/replication | n/a |

### Resources

| Name | Type |
| ---- | ---- |
| [random_id.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/id) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_role\_arn | ARN of the IAM role artifactweave runs as (its instance profile's or workload's role). Both bucket policies grant it their objects: the primary's in normal running, the replica's after a failover. | `string` | n/a | yes |
| expiring\_prefixes | Key prefixes, from each bucket's root, whose objects expire after the given number of days, for example { "scratch/" = 1 }. | `map(number)` | `{}` | no |
| force\_destroy | Let terraform destroy delete the buckets with every object version still in them. Off, a destroy fails while a bucket holds anything. | `bool` | `false` | no |
| iam\_role\_path | IAM path of the replication role, beginning and ending with /. Null leaves IAM's default, /. | `string` | `null` | no |
| name | Start of every name: the buckets are <name>-<8 hex> and <name>-<8 hex>-r, within S3's 63 characters, and the replication role <name, cut to 40 characters>-<8 hex>-repl. | `string` | n/a | yes |
| permissions\_boundary\_arn | Permissions boundary for the replication role, when the deployer may create roles only with one attached. | `string` | `null` | no |
| region | Region of the primary bucket, where artifactweave writes and signs download URLs (blob.s3.region). | `string` | n/a | yes |
| replica\_region | Region of the replica bucket, where S3 copies the committed blobs. | `string` | n/a | yes |
| replication\_time\_control | S3 Replication Time Control: 99.9% of objects replicated within 15 minutes, with replication metrics, billed per GB replicated. | `bool` | `false` | no |
| store\_prefix | Key prefix artifactweave writes under (blob.s3.prefix). Keys under <store\_prefix>sha256/, the committed blobs, replicate. | `string` | `""` | no |
| tags | Tags for both buckets and the replication role. | `map(string)` | `{}` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| artifactweave\_config | The blob section of artifactweave.yaml: one store, the primary bucket. It authenticates through the AWS SDK chain, as the app role. |
| bucket | The primary bucket, for blob.s3.bucket. |
| expires\_at | For the test harness: the expires-at tag (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise. |
| failover\_artifactweave\_config | The same blob section pointed at the replica, for a failover: re-point artifactweave here and restart it. |
| region | The primary bucket's region, for blob.s3.region. |
| replica\_bucket | The replica bucket S3 copies the committed blobs into. |
| replica\_region | The replica bucket's region. |
| replicated\_prefix | Keys under this prefix, the committed blobs, replicate. |
| storage\_host | Where a downloadRedirect repository's presigned URLs point: the primary bucket's virtual-hosted endpoint. |
<!-- END_TF_DOCS -->
