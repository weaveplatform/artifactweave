# gcp/multi-region: a mirror across Cloud Storage regions

Regional Cloud Storage buckets in two or more locations as members of one artifactweave mirror
(decision [0028](../../../../../docs/research/decisions/0028-blob-mirroring.md)). Every blob is
written to each bucket, an upload succeeds once `write_quorum` of them hold it, and reads
prefer the bucket in this instance's zone. Choose it for regional resilience under
artifactweave's control: a region can be lost without losing a blob or stopping uploads, and a
redirect only ever points at a copy that exists. Choose `geo-replicated` instead to have Cloud
Storage replicate one dual-region bucket on its own, or `single-bucket` when one region is
enough.

## What it deploys

- One bucket per entry of `locations`, each through
  [`modules/gcp/storage`](../../modules/gcp/storage/README.md), named
  `<name>-<suffix>-<location>`, with object rights for `app_service_account`.
- `random_id.suffix`: eight hex characters shared by the bucket names, so they are globally
  unique.

No bucket replicates on its own. A dual-region bucket's copies are invisible to the mirror and
never count toward its quorum.

## How it behaves

- **Uploads** go to every member in parallel and succeed once `write_quorum` of them hold the
  blob. The other copies finish within `blob.mirror.member_timeout` (30 s by default), or
  repair copies them later.
- **Reads and redirects** prefer the member in this instance's zone, the first location, and
  fall back to another when a member fails. A `downloadRedirect` repository signs a URL only
  on a member that holds the blob, so a redirect never lands on a missing copy.
- **Draining** a member (`write_enabled: false` on its store) stops new copies on it while
  reads go on; enabling it again lets repair copy what it missed.
- **Losing a region** stops nothing with `write_quorum: 1`: uploads and reads carry on in the
  others, and repair fills the gap once the region is back.

## Prerequisites

- Terraform 1.9 or later, and Application Default Credentials (`gcloud auth
  application-default login`) for a principal that can create buckets and set their IAM
  policy in `project_id`, where the Cloud Storage API is enabled.
- The service account artifactweave runs as, created elsewhere (a workload identity's, or a
  VM's). This scenario grants it object rights on the buckets; it does not create it. It
  signs download URLs through the IAM Credentials API, so it needs
  `roles/iam.serviceAccountTokenCreator` on itself.

### Getting the code

Copy this directory together with `modules/gcp/` and `.terraform.lock.hcl`, keeping the
`../../modules` layout, or point the module at the repository:

```hcl
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/gcp/storage?ref=v0.1.0" # x-release-please-version
```

## Inputs to set

```hcl
# terraform.tfvars
project_id          = "example-project"
name                = "artifactweave-example"
locations           = ["europe-west2", "europe-west1"]
app_service_account = "artifactweave@example-project.iam.gserviceaccount.com"

# Optional
# store_prefix      = "artifacts/"
# write_quorum      = 2
# expiring_prefixes = { "scratch/" = 1 }
# force_destroy     = true
# labels            = { team = "platform" }
```

```bash
terraform init
terraform apply
```

## The artifactweave configuration

`artifactweave_config` renders the `blob` section of `artifactweave.yaml`: one `gcs` store per
bucket, named after its location, with its location as its `zone`, and a mirror over them
whose `zone` is the first location. Each store signs download URLs as `app_service_account`
(`google_access_id`) and names no key: Application Default Credentials supply the identity.
The example below is the rendered output for the example inputs above, with the random suffix
`0a1b2c3d`; the `readme_example` test fails if they differ.

<!-- BEGIN_CONFIG_EXAMPLE -->
```yaml
blob:
  backend: mirror
  stores:
    gcs_europe_west2:
      backend: gcs
      zone: europe-west2
      gcs:
        bucket: artifactweave-example-0a1b2c3d-europe-west2
        google_access_id: artifactweave@example-project.iam.gserviceaccount.com
    gcs_europe_west1:
      backend: gcs
      zone: europe-west1
      gcs:
        bucket: artifactweave-example-0a1b2c3d-europe-west1
        google_access_id: artifactweave@example-project.iam.gserviceaccount.com
  mirror:
    members:
      - gcs_europe_west2
      - gcs_europe_west1
    zone: europe-west2
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

Run it as `app_service_account`, through workload identity or a VM's service account, so
Application Default Credentials find it; set `blob.mirror.zone` on each instance to the
location it runs in. A `downloadRedirect` repository answers downloads with a 302 to a signed
URL on the member that holds the blob, preferring the zone's; every member's URLs are on
`storage_host`, `storage.googleapis.com`, path-style. The
[configuration reference](../../../../../docs/operations/configuration-reference.md) lists
every key.

## Security

Each bucket carries the [`modules/gcp/storage` invariants](../../modules/gcp/storage/README.md#invariants):
public access prevention enforced, uniform bucket-level access, Google-managed encryption,
and abandoned uploads expired. The app service account can create, read, list and delete
objects in these buckets, and nothing else: its grant is on each bucket, not the project, and
it cannot change a bucket's settings or IAM policy.

### Accepted findings

| ID | Resource (module) | Why |
|---|---|---|
| GCP-0007 | `google_storage_bucket_iam_member.app` (`gcp/storage`) | `roles/storage.objectAdmin` is bound to one bucket, not the project, and the backend needs all of it, deleting the parts it composes included. |
| GCP-0066 | `google_storage_bucket.blobs` (`gcp/storage`) | Google-managed encryption keys; a customer-managed key would add a KMS key ring and its grants to manage. |
| GCP-0077 | `google_storage_bucket.blobs` (`gcp/storage`) | No access-log bucket: Cloud Audit Logs record the administrative calls. |
| GCP-0078 | `google_storage_bucket.blobs` (`gcp/storage`) | No versioning: blobs are content-addressed and never rewritten, and the mirror, not Cloud Storage, keeps the second copy. |

## Cost

### Steady state

Storage is billed once per bucket: two locations store everything twice (Standard storage,
roughly $0.020 to $0.023 per GB-month in the European regions). artifactweave writes each copy
from where it runs, so the copy to a farther region, and every repair copy, is inter-region
network transfer (roughly $0.02 per GB within Europe). Operations are billed per bucket, and
downloads leaving Google as internet egress. There is no fixed or hourly charge.

### Per test run

About one to two cents: a proof stores well under 100 MiB for minutes, and its cost is
mostly the test's downloads leaving Google and its operations.

## Teardown

`terraform destroy` removes the buckets and their grants. It fails on a bucket that still
holds objects unless `force_destroy` is true, which deletes them with the bucket. Nothing
outlives the scenario: the app service account is not this scenario's.

## How this scenario is tested

- **Mocked tests** (`tests/scenario.tftest.hcl`, in `make deploy-check`) check the
  composition: `readme_example` renders the example above from the example inputs; `ci_run`
  uses the longest instance a proof produces, checking that bucket names fit 63 characters
  and the outputs reap and `test/platform` read; others check the inputs' validation.
- **The real-cloud proof** (`platform-scenarios.yml`, on a pull request that changes this
  scenario or its module) applies an instance as the CI deployer, which also runs
  artifactweave, and runs `test/platform`: both members hold each blob, a redirect prefers
  the zone's member, an upload and every read succeed with that member drained, and repair
  copies the new blob back once it is enabled again. It then destroys the instance, applies
  and destroys another from its remote state alone, and checks that reap removes an expired
  one.
- **Harness outputs** (`harness.tf`): `expires_at`, the label reap reads from the state, and
  `mirror_members`, the members `test/platform` checks redirects against.
- **Locally**, `make apply`, `up`, `smoke`, `verify` and `destroy` run the same checks
  against an instance in the lab ([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)).

| Variable | Scenario run (`ci-vars.sh`) | Operator run (`Makefile`, `deploy/ci/lab-vars.sh`) |
|---|---|---|
| `project_id` | `PLATFORM_GCP_PROJECT` | `LAB_GCP_PROJECT` |
| `name` | `${NAME_PREFIX}${INSTANCE}`: `awscn-<instance>`, the start reap knows scenario names by | `artifactweave-mr` |
| `locations` | `europe-west2`, then `europe-west1` | `europe-west2`, then `europe-west1` |
| `app_service_account` | `PLATFORM_GCP_SERVICE_ACCOUNT`, the deployer | the bootstrap's conformance service account |
| `labels` | `$SCENARIO_LABELS`: `artifactweave-scenario`, `instance`, `expires-at` | none |
| `force_destroy` | `true` | `true` |

## Reference

Generated by terraform-docs (`make deploy-docs`).

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.9 |
| google | ~> 8.6 |
| random | ~> 3.9 |

### Providers

| Name | Version |
| ---- | ------- |
| random | 3.9.1 |

### Modules

| Name | Source | Version |
| ---- | ------ | ------- |
| member | ../../modules/gcp/storage | n/a |

### Resources

| Name | Type |
| ---- | ---- |
| [random_id.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/id) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_service\_account | Email of the service account artifactweave runs as. It gets object rights on every bucket, and signs download URLs (blob.stores.<name>.gcs.google\_access\_id). | `string` | n/a | yes |
| expiring\_prefixes | Object prefixes, from each bucket's root, whose objects expire after the given number of days, for example { "scratch/" = 1 }. | `map(number)` | `{}` | no |
| force\_destroy | Let terraform destroy delete the buckets with every object still in them. Off, a destroy fails while a bucket holds anything. | `bool` | `false` | no |
| labels | Labels for every bucket. | `map(string)` | `{}` | no |
| locations | Regions of the mirror's members, one bucket each, in order. The first is this instance's zone (blob.mirror.zone): reads prefer it. | `list(string)` | n/a | yes |
| name | Start of every bucket name. Each bucket is <name>-<8 hex>-<location>, which must fit Cloud Storage's 63 characters. | `string` | n/a | yes |
| project\_id | Project that holds the buckets. Nothing is created anywhere else. | `string` | n/a | yes |
| store\_prefix | Object prefix artifactweave writes under in each bucket (blob.stores.<name>.gcs.prefix). Empty writes at the bucket's root. | `string` | `""` | no |
| write\_quorum | Copies stored before an upload succeeds (blob.mirror.write\_quorum). 1 keeps uploads going with a region down; the other copies follow in the background or through repair. | `number` | `1` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| artifactweave\_config | The blob section of artifactweave.yaml: a mirror of the buckets, preferring the first location. Each store authenticates with Application Default Credentials, as the app service account, which also signs download URLs. |
| buckets | The member buckets, by location. |
| expires\_at | For the test harness: the expires-at label (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise. |
| mirror\_members | For the test harness: the mirror's members in order (this instance's zone first), each with its store name, zone, and what a signed URL for one of its objects starts with after the scheme. test/platform checks which member a redirect lands on with it. |
| storage\_host | Where a downloadRedirect repository's signed URLs point: Cloud Storage's path-style endpoint, the same for every member. |
<!-- END_TF_DOCS -->
