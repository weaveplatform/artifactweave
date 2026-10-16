# aws/single-bucket: one private S3 bucket

One private S3 bucket whose bucket policy grants the IAM role artifactweave runs as the
bucket's objects: the smallest deployment of artifactweave's S3 backend. Choose it when one
region is enough and S3's own durability within it is what you need. Choose `multi-region`
for copies in several regions under artifactweave's control, or `geo-replicated` to have S3
replicate the bucket into a second region.

## What it deploys

- One bucket through [`modules/aws/storage`](../../modules/aws/storage/README.md), named
  `<name>-<suffix>`, with a bucket policy granting `app_role_arn` its objects.
- `random_id.suffix`: eight hex characters in the bucket name, so it is globally unique.

## How it behaves

- **Uploads** stream into the bucket under `store_prefix`: parts land under `_uploads/`, and
  the committed blob under `sha256/`.
- **Reads** come from the bucket. A `downloadRedirect` repository answers a download with a
  302 to a presigned URL on the bucket's virtual-hosted endpoint, so the bytes go from S3 to
  the client.
- **Losing the region** stops uploads and downloads until it is back. S3 keeps the objects
  across availability zones within it.

## Prerequisites

- Terraform 1.9 or later, and AWS credentials the SDK chain finds (`aws login`, a profile, a
  CI role) for the account that will hold the bucket.
- Rights to create an S3 bucket, its policy, encryption, lifecycle and public access
  settings.
- The IAM role artifactweave runs as, created elsewhere (an EC2 instance profile's role, an
  EKS Pod Identity role). This scenario grants it the bucket's objects; it does not create
  it.

### Getting the code

Copy this directory together with `modules/aws/` and `.terraform.lock.hcl`, keeping the
`../../modules` layout, or point the module at the repository:

```hcl
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/aws/storage?ref=v0.0.0" # x-release-please-version
```

## Inputs to set

```hcl
# terraform.tfvars
name         = "artifactweave-example"
region       = "eu-west-2"
app_role_arn = "arn:aws:iam::111122223333:role/artifactweave"

# Optional
# store_prefix      = "artifacts/"
# expiring_prefixes = { "scratch/" = 1 }
# force_destroy     = true
# tags              = { team = "platform" }
```

```bash
terraform init
terraform apply
```

## The artifactweave configuration

`artifactweave_config` renders the `blob` section of `artifactweave.yaml`: one `s3` store, the
bucket, in its region, with `prefix` only when `store_prefix` is set. The store names no
credentials: the AWS SDK chain supplies the app role's. The example below is the rendered
output for the example inputs above, with the random suffix `0a1b2c3d`; the `readme_example`
test fails if they differ.

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
URL on `storage_host`, the bucket's virtual-hosted endpoint. The
[configuration reference](../../../../../docs/operations/configuration-reference.md) lists
every key.

## Security

The bucket carries the [`modules/aws/storage` invariants](../../modules/aws/storage/README.md#invariants):
public access blocked, ACLs disabled, TLS required, S3-managed encryption, and abandoned
uploads expired. The app role can read, write, list and delete objects in this bucket through
its bucket policy, and nothing else: it cannot change the bucket's settings or policy, and its
rights end with the bucket.

### Accepted findings

| ID | Resource (module) | Why |
|---|---|---|
| AWS-0089 | `aws_s3_bucket.blobs` (`aws/storage`) | No access logging: CloudTrail records the management calls, and server access logs need a second bucket. |
| AWS-0090 | `aws_s3_bucket.blobs` (`aws/storage`) | No versioning: blobs are content-addressed and never rewritten, and versioning would keep every deleted upload part. |
| AWS-0132 | `aws_s3_bucket_server_side_encryption_configuration.blobs` (`aws/storage`) | S3-managed keys; a KMS key would add a monthly fee and a key policy to manage. |

## Cost

### Steady state

S3 Standard storage, roughly $0.023 to $0.024 per GB-month in the European regions, plus
requests and downloads leaving AWS as internet egress. There is no fixed or hourly charge.

### Per test run

About one cent: a proof stores well under 100 MiB for minutes, and its cost is mostly the
test's downloads leaving AWS and its requests. A conformance run adds a few cents of
requests.

## Teardown

`terraform destroy` removes the bucket and its policy. It fails while the bucket holds
objects unless `force_destroy` is true, which deletes them with the bucket. Nothing outlives
the scenario: the app role is not this scenario's.

## How this scenario is tested

- **Mocked tests** (`tests/scenario.tftest.hcl`, in `make deploy-check`) check the
  composition: `readme_example` renders the example above from the example inputs; `ci_run`
  uses the longest instance a proof produces, checking that the bucket name fits 63
  characters and the outputs reap and `test/platform` read; `lab_run` checks that the lab's
  conformance environment keeps its bucket name and prefix; others
  check the inputs' validation.
- **The real-cloud proof** (`platform-scenarios.yml`, on a pull request that changes this
  scenario, its module or the scenario driver) applies an instance as the CI deployer, which
  also runs artifactweave, and runs `test/platform`: a round trip, and a `downloadRedirect`
  302 that lands on `storage_host` and serves the same bytes. It then destroys the instance,
  applies and destroys another from its remote state alone, and checks that reap removes an
  expired one.
- **The real-cloud conformance tests** (`cloud-conformance.yml`, job `s3`) run the S3
  backend's contract and probes against the lab's instance, under `conformance/`, as the
  bootstrap's conformance role. `make github-vars` sets `CONFORMANCE_S3_BUCKET` from the
  `bucket` output, and `make conformance` runs the tests as your own login.
- **Harness outputs** (`harness.tf`): `expires_at`, the tag reap reads from the state.
- **Locally**, `make apply`, `up`, `smoke`, `verify` and `destroy` run the same checks
  against the lab's instance ([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)).
  `moved.tf` keeps that instance's bucket, created before this scenario composed the module.

| Variable | Scenario run (`ci-vars.sh`) | Operator run (`Makefile`, `deploy/ci/lab-vars.sh`) |
|---|---|---|
| `name` | `${NAME_PREFIX}${INSTANCE}`: `artifactweave-awscn-<instance>`, inside the bucket names the deployer may manage | `artifactweave-acc` |
| `region` | `PLATFORM_AWS_REGION` | `LAB_AWS_REGION` |
| `app_role_arn` | `PLATFORM_AWS_ROLE_ARN`, the deployer | the bootstrap's conformance role, in the signed-in account |
| `store_prefix` | none: the bucket's root | `local/`, where a local run writes |
| `expiring_prefixes` | none | `{ "conformance/" = 1 }`: what an interrupted conformance run leaves expires after a day |
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
| storage | ../../modules/aws/storage | n/a |

### Resources

| Name | Type |
| ---- | ---- |
| [random_id.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/id) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_role\_arn | ARN of the IAM role artifactweave runs as (its instance profile's or workload's role). The bucket policy grants it the bucket's objects. | `string` | n/a | yes |
| expiring\_prefixes | Key prefixes, from the bucket's root, whose objects expire after the given number of days, for example { "scratch/" = 1 }. | `map(number)` | `{}` | no |
| force\_destroy | Let terraform destroy delete the bucket with every object still in it. Off, a destroy fails while the bucket holds anything. | `bool` | `false` | no |
| name | Start of the bucket name, which is <name>-<8 hex> and must fit S3's 63 characters. | `string` | n/a | yes |
| region | Region of the bucket (blob.s3.region). | `string` | n/a | yes |
| store\_prefix | Key prefix artifactweave writes under (blob.s3.prefix). Empty writes at the bucket's root. | `string` | `""` | no |
| tags | Tags for the bucket. | `map(string)` | `{}` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| artifactweave\_config | The blob section of artifactweave.yaml for the bucket. The store authenticates through the AWS SDK chain, as the app role. |
| bucket | The bucket, for blob.s3.bucket and the real-cloud conformance tests (ARTIFACTWEAVE\_TEST\_S3\_BUCKET). |
| expires\_at | For the test harness: the expires-at tag (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise. |
| region | The bucket's region, for blob.s3.region and the real-cloud conformance tests (ARTIFACTWEAVE\_TEST\_S3\_REGION). |
| storage\_host | Where a downloadRedirect repository's presigned URLs point: the bucket's virtual-hosted endpoint. |
<!-- END_TF_DOCS -->
