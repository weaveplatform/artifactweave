# aws/multi-region: a mirror across S3 regions

S3 buckets in two or more regions as members of one artifactweave mirror (decision
[0028](../../../../../docs/research/decisions/0028-blob-mirroring.md)). Every blob is written
to each bucket, an upload succeeds once `write_quorum` of them hold it, and reads prefer the
bucket in this instance's zone. Choose it for regional resilience under artifactweave's
control: a region can be lost without losing a blob or stopping uploads, and a redirect only
ever points at a copy that exists. Choose `geo-replicated` instead to have S3 replicate one
bucket on its own, or `single-bucket` when one region is enough.

## What it deploys

- One bucket per entry of `regions`, each through
  [`modules/aws/storage`](../../modules/aws/storage/README.md), named
  `<name>-<suffix>-<region code>` (`eu-west-2` becomes `euw2`), with a bucket policy granting
  `app_role_arn` that bucket's objects.
- `random_id.suffix`: eight hex characters shared by the bucket names, so they are globally
  unique.

No bucket replicates on its own. S3 replication's copies are invisible to the mirror and
never count toward its quorum.

## How it behaves

- **Uploads** go to every member in parallel and succeed once `write_quorum` of them hold the
  blob. The other copies finish within `blob.mirror.member_timeout` (30 s by default), or
  repair copies them later.
- **Reads and redirects** prefer the member in this instance's zone, the first region, and
  fall back to another when a member fails. A `downloadRedirect` repository signs a URL only
  on a member that holds the blob, so a redirect never lands on a missing copy.
- **Draining** a member (`write_enabled: false` on its store) stops new copies on it while
  reads go on; enabling it again lets repair copy what it missed.
- **Losing a region** stops nothing with `write_quorum: 1`: uploads and reads carry on in the
  others, and repair fills the gap once the region is back.

## Prerequisites

- Terraform 1.9 or later, and AWS credentials the SDK chain finds (`aws login`, a profile, a
  CI role) for the account that will hold the buckets.
- Rights to create S3 buckets, their policies, encryption, lifecycle and public access
  settings in each region.
- The IAM role artifactweave runs as, created elsewhere (an EC2 instance profile's role, an
  EKS Pod Identity role). This scenario grants it the buckets' objects; it does not create it.

### Getting the code

Copy this directory together with `modules/aws/` and `.terraform.lock.hcl`, keeping the
`../../modules` layout, or point the module at the repository:

```hcl
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/aws/storage?ref=v0.1.1" # x-release-please-version
```

## Inputs to set

```hcl
# terraform.tfvars
name         = "artifactweave-example"
regions      = ["eu-west-2", "eu-west-1"]
app_role_arn = "arn:aws:iam::111122223333:role/artifactweave"

# Optional
# store_prefix      = "artifacts/"
# write_quorum      = 2
# expiring_prefixes = { "scratch/" = 1 }
# force_destroy     = true
# tags              = { team = "platform" }
```

```bash
terraform init
terraform apply
```

## The artifactweave configuration

`artifactweave_config` renders the `blob` section of `artifactweave.yaml`: one `s3` store per
bucket, named after its region, with its region as its `zone`, and a mirror over them whose
`zone` is the first region. The stores name no credentials: the AWS SDK chain supplies the
app role's. The example below is the rendered output for the example inputs above, with the
random suffix `0a1b2c3d`; the `readme_example` test fails if they differ.

<!-- BEGIN_CONFIG_EXAMPLE -->
```yaml
blob:
  backend: mirror
  stores:
    s3_eu_west_2:
      backend: s3
      zone: eu-west-2
      s3:
        bucket: artifactweave-example-0a1b2c3d-euw2
        region: eu-west-2
    s3_eu_west_1:
      backend: s3
      zone: eu-west-1
      s3:
        bucket: artifactweave-example-0a1b2c3d-euw1
        region: eu-west-1
  mirror:
    members:
      - s3_eu_west_2
      - s3_eu_west_1
    zone: eu-west-2
    write_quorum: 1
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
its credentials; set `blob.mirror.zone` on each instance to the region it runs in. A
`downloadRedirect` repository answers downloads with a 302 to a presigned URL on the member
that holds the blob, preferring the zone's: `storage_host` is the first member's endpoint. The
[configuration reference](../../../../../docs/operations/configuration-reference.md) lists
every key.

## Security

Each bucket carries the [`modules/aws/storage` invariants](../../modules/aws/storage/README.md#invariants):
public access blocked, ACLs disabled, TLS required, S3-managed encryption, and abandoned
uploads expired. The app role can read, write, list and delete objects in these buckets
through their bucket policies, and nothing else: it cannot change a bucket's settings or
policy, and its rights end with the buckets.

### Accepted findings

| ID | Resource (module) | Why |
|---|---|---|
| AWS-0089 | `aws_s3_bucket.blobs` (`aws/storage`) | No access logging: CloudTrail records the management calls, and server access logs need a second bucket. |
| AWS-0090 | `aws_s3_bucket.blobs` (`aws/storage`) | No versioning: blobs are content-addressed and never rewritten, and the mirror, not S3, keeps the second copy. |
| AWS-0132 | `aws_s3_bucket_server_side_encryption_configuration.blobs` (`aws/storage`) | S3-managed keys; a KMS key would add a monthly fee and a key policy to manage. |

## Cost

### Steady state

Storage is billed once per bucket: two regions store everything twice (S3 Standard, roughly
$0.023 to $0.024 per GB-month in the European regions). artifactweave writes each copy from
where it runs, so the copy to a farther region, and every repair copy, is inter-region data
transfer (roughly $0.02 per GB). Requests are billed per bucket, and downloads leaving AWS as
internet egress. There is no fixed or hourly charge.

### Per test run

About one cent: a proof stores well under 100 MiB for minutes, and its cost is mostly the
test's downloads leaving AWS and its requests.

## Teardown

`terraform destroy` removes the buckets and their policies. It fails on a bucket that still
holds objects unless `force_destroy` is true, which deletes them with the bucket. Nothing
outlives the scenario: the app role is not this scenario's.

## How this scenario is tested

- **Mocked tests** (`tests/scenario.tftest.hcl`, in `make deploy-check`) check the
  composition: `readme_example` renders the example above from the example inputs;
  `ci_run` uses the longest instance a proof produces, checking that bucket names fit 63
  characters and the outputs reap and `test/platform` read; others check region codes and
  the inputs' validation.
- **The real-cloud proof** (`platform-scenarios.yml`, on a pull request that changes this
  scenario or its module) applies an instance as the CI deployer, which also runs
  artifactweave, and runs `test/platform`: both members hold each blob, a redirect prefers
  the zone's member, an upload and every read succeed with that member drained, and repair
  copies the new blob back once it is enabled again. It then destroys the instance, applies
  and destroys another from its remote state alone, and checks that reap removes an expired
  one.
- **Harness outputs** (`harness.tf`): `expires_at`, the tag reap reads from the state, and
  `mirror_members`, the members `test/platform` checks redirects against.
- **Locally**, `make apply`, `up`, `smoke`, `verify` and `destroy` run the same checks
  against an instance in the lab ([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)).

| Variable | Scenario run (`ci-vars.sh`) | Operator run (`Makefile`, `deploy/ci/lab-vars.sh`) |
|---|---|---|
| `name` | `${NAME_PREFIX}${INSTANCE}`: `artifactweave-awscn-<instance>`, inside the bucket names the deployer may manage | `artifactweave-mr` |
| `regions` | `PLATFORM_AWS_REGION`, then `eu-west-1` | `LAB_AWS_REGION`, then `eu-west-1` |
| `app_role_arn` | `PLATFORM_AWS_ROLE_ARN`, the deployer | the bootstrap's conformance role, in the signed-in account |
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
| member | ../../modules/aws/storage | n/a |

### Resources

| Name | Type |
| ---- | ---- |
| [random_id.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/id) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_role\_arn | ARN of the IAM role artifactweave runs as (its instance profile's or workload's role). Every bucket policy grants it that bucket's objects. | `string` | n/a | yes |
| expiring\_prefixes | Key prefixes, from each bucket's root, whose objects expire after the given number of days, for example { "scratch/" = 1 }. | `map(number)` | `{}` | no |
| force\_destroy | Let terraform destroy delete the buckets with every object still in them. Off, a destroy fails while a bucket holds anything. | `bool` | `false` | no |
| name | Start of every bucket name. Each bucket is <name>-<8 hex>-<region code> (eu-central-1 becomes euc1), which must fit S3's 63 characters. | `string` | n/a | yes |
| regions | Regions of the mirror's members, one bucket each, in order. The first is this instance's zone (blob.mirror.zone): reads prefer it, and its bucket signs download URLs. | `list(string)` | n/a | yes |
| store\_prefix | Key prefix artifactweave writes under in each bucket (blob.stores.<name>.s3.prefix). Empty writes at the bucket's root. | `string` | `""` | no |
| tags | Tags for every bucket. | `map(string)` | `{}` | no |
| write\_quorum | Copies stored before an upload succeeds (blob.mirror.write\_quorum). 1 keeps uploads going with a region down; the other copies follow in the background or through repair. | `number` | `1` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| artifactweave\_config | The blob section of artifactweave.yaml: a mirror of the buckets, preferring the first region. Each store authenticates through the AWS SDK chain, as the app role. |
| buckets | The member buckets, by region. |
| expires\_at | For the test harness: the expires-at tag (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise. |
| mirror\_members | For the test harness: the mirror's members in order (this instance's zone first), each with its store name, zone, and what a presigned URL for one of its objects starts with after the scheme. test/platform checks which member a redirect lands on with it. |
| storage\_host | Where a downloadRedirect repository's presigned URLs point: the first member's endpoint, which signs once it holds the blob. |
<!-- END_TF_DOCS -->
