# gcp/geo-replicated: one dual-region bucket that Cloud Storage replicates

One Cloud Storage bucket that Cloud Storage replicates across two regions, a dual-region,
as artifactweave's `gcs` store. Choose it to have the cloud keep a copy in a second region
with no failover step: the bucket name, the configuration and the signed URLs stay the same
whichever region serves them. Choose `multi-region` instead for a mirror across regions under
artifactweave's control, where an upload succeeds only once the copies it needs exist, or
`single-bucket` when one region is enough.

## What it deploys

- One bucket through [`modules/gcp/storage`](../../modules/gcp/storage/README.md), named
  `<name>-<suffix>`, in the dual-region `location` (or the two `data_locations` inside it),
  with object rights for `app_service_account`.
- `random_id.suffix`: eight hex characters in the bucket name, so it is globally unique.

## How it behaves

- **One store to artifactweave.** It writes, reads and signs URLs against the bucket as it
  would any bucket, and Cloud Storage keeps a copy in each region. The second region never
  appears in a mirror's replica index and never counts toward a mirror's write quorum
  (decision [0028](../../../../../docs/research/decisions/0028-blob-mirroring.md)); if you
  also run a mirror, treat this bucket as one member.
- **Recovery point objective.** By default, 99.9% of newly written objects reach the second
  region within an hour and all of them within 12 hours. Turbo replication
  (`turbo_replication = true`) targets 15 minutes
  ([research 20](../../../../../docs/research/20-reference-deployments.md#22-google-cloud)).
- **No failover step.** Cloud Storage is strongly consistent and the bucket name does not
  change, so the same configuration and the same signed URLs keep working if a region goes
  down. Objects written within the recovery point window may be unavailable until it
  returns.
- **Uploads and redirects** work as on any bucket: a large upload is composed from parts
  under `<store_prefix>_uploads/`, and a `downloadRedirect` repository answers a download
  with a 302 to a signed URL on `storage.googleapis.com`.

## Prerequisites

- Terraform 1.9 or later, and Application Default Credentials (`gcloud auth
  application-default login`) for a principal that can create buckets and set their IAM
  policy in `project_id`, where the Cloud Storage API is enabled.
- The service account artifactweave runs as, created elsewhere (a workload identity's, or a
  VM's). This scenario grants it object rights on the bucket; it does not create it. It
  signs download URLs through the IAM Credentials API, so it needs
  `roles/iam.serviceAccountTokenCreator` on itself.

### Getting the code

Copy this directory together with `modules/gcp/` and `.terraform.lock.hcl`, keeping the
`../../modules` layout, or point the module at the repository:

```hcl
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/gcp/storage?ref=v0.1.1" # x-release-please-version
```

## Inputs to set

```hcl
# terraform.tfvars
project_id          = "example-project"
name                = "artifactweave-example"
location            = "EUR4"
app_service_account = "artifactweave@example-project.iam.gserviceaccount.com"

# Optional
# location          = "EU"                                # a configurable dual-region:
# data_locations    = ["europe-west1", "europe-west4"]    # these two regions inside EU
# turbo_replication = true
# store_prefix      = "artifacts/"
# expiring_prefixes = { "scratch/" = 1 }
# force_destroy     = true
# labels            = { team = "platform" }
```

```bash
terraform init
terraform apply
```

## The artifactweave configuration

`artifactweave_config` renders the `blob` section of `artifactweave.yaml`: one `gcs` store on
the bucket, whichever regions hold it, which signs download URLs as `app_service_account`
(`google_access_id`) and names no key: Application Default Credentials supply the identity.
`prefix` appears only when `store_prefix` is set. The example below is the rendered output
for the example inputs above, with the random suffix `0a1b2c3d`; the `readme_example` test
fails if they differ.

<!-- BEGIN_CONFIG_EXAMPLE -->
```yaml
blob:
  backend: gcs
  gcs:
    bucket: artifactweave-example-0a1b2c3d
    google_access_id: artifactweave@example-project.iam.gserviceaccount.com
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
Application Default Credentials find it. Instances in either region use the same
configuration. A `downloadRedirect` repository answers downloads with a 302 to a signed URL
on `storage_host`, `storage.googleapis.com`, path-style, which serves the bucket from
whichever region is up. The
[configuration reference](../../../../../docs/operations/configuration-reference.md) lists
every key.

## Security

The bucket carries the [`modules/gcp/storage` invariants](../../modules/gcp/storage/README.md#invariants):
public access prevention enforced, uniform bucket-level access, Google-managed encryption,
and abandoned uploads expired. The app service account can create, read, list and delete
objects in this bucket, in both regions, and nothing else: its grant is on the bucket, not the
project, and it cannot change the bucket's settings or IAM policy.

### Accepted findings

| ID | Resource (module) | Why |
|---|---|---|
| GCP-0007 | `google_storage_bucket_iam_member.app` (`gcp/storage`) | `roles/storage.objectAdmin` is bound to one bucket, not the project, and the backend needs all of it, deleting the parts it composes included. |
| GCP-0066 | `google_storage_bucket.blobs` (`gcp/storage`) | Google-managed encryption keys; a customer-managed key would add a KMS key ring in both regions and its grants to manage. |
| GCP-0077 | `google_storage_bucket.blobs` (`gcp/storage`) | No access-log bucket: Cloud Audit Logs record the administrative calls. |
| GCP-0078 | `google_storage_bucket.blobs` (`gcp/storage`) | No versioning: blobs are content-addressed and never rewritten, and the second region, not object versions, keeps the second copy. |

## Cost

### Steady state

Dual-region storage costs more per GB-month than one region, and Cloud Storage charges
replication to the second region per GB written; turbo replication adds a further per-GB
charge. Operations are billed per thousand, and downloads leaving Google as internet egress.
There is no fixed or hourly charge.

### Per test run

About a cent: a proof stores well under 100 MiB for minutes, and its cost is mostly the
test's downloads leaving Google, its operations and the replication of what it writes.

## Teardown

`terraform destroy` removes the bucket, in both regions, and its grant. It fails while the
bucket holds objects unless `force_destroy` is true, which deletes them with the bucket.
Nothing outlives the scenario: the app service account is not this scenario's.

## How this scenario is tested

- **Mocked tests** (`tests/scenario.tftest.hcl`, in `make deploy-check`) check the
  composition: `readme_example` renders the example above from the example inputs; `ci_run`
  uses the longest instance a proof produces, checking that the bucket name fits 63
  characters and the outputs reap and `test/platform` read; others check that turbo
  replication is opt-in and that a single region, or one data location, is refused.
- **The real-cloud proof** (`platform-scenarios.yml`, on a pull request that changes this
  scenario, its module or `test/platform/replica_test.go`) applies an instance as the CI
  deployer, which also runs artifactweave, and runs `test/platform`: the generic checks (a
  40 MiB deploy, round trip, ranged reads, checksums and a signed `downloadRedirect`), then
  a read of the real bucket's placement that fails unless it is a dual-region. Cloud Storage
  exposes no per-object replication status, so that is as far as the proof can see. It then
  destroys the instance, applies and destroys another from its remote state alone, and
  checks that reap removes an expired one.
- **Harness outputs** (`harness.tf`): `expires_at`, the label reap reads from the state.
- **Locally**, `make apply`, `up`, `smoke`, `verify` and `destroy` run the same checks
  against an instance in the lab ([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)).

| Variable | Scenario run (`ci-vars.sh`) | Operator run (`Makefile`, `deploy/ci/lab-vars.sh`) |
|---|---|---|
| `project_id` | `PLATFORM_GCP_PROJECT` | `LAB_GCP_PROJECT` |
| `name` | `${NAME_PREFIX}${INSTANCE}`: `awscn-<instance>`, the start reap knows scenario names by | `artifactweave-geo` |
| `location` | `EUR4` | `EUR4` |
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
| bucket | ../../modules/gcp/storage | n/a |

### Resources

| Name | Type |
| ---- | ---- |
| [random_id.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/id) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_service\_account | Email of the service account artifactweave runs as. It gets object rights on the bucket, and signs download URLs (blob.gcs.google\_access\_id). | `string` | n/a | yes |
| data\_locations | The two regions of a configurable dual-region inside location, for example ["europe-west1", "europe-west4"] with location EU. Empty uses the predefined dual-region in location. | `list(string)` | `[]` | no |
| expiring\_prefixes | Object prefixes, from the bucket's root, whose objects expire after the given number of days, for example { "scratch/" = 1 }. | `map(number)` | `{}` | no |
| force\_destroy | Let terraform destroy delete the bucket with every object still in it. Off, a destroy fails while the bucket holds anything. | `bool` | `false` | no |
| labels | Labels for the bucket. | `map(string)` | `{}` | no |
| location | A predefined dual-region such as EUR4 (Finland and the Netherlands), or a multi-region such as EU together with data\_locations for a configurable dual-region. | `string` | n/a | yes |
| name | Start of the bucket's name. The bucket is <name>-<8 hex>, which must fit Cloud Storage's 63 characters. | `string` | n/a | yes |
| project\_id | Project that holds the bucket. Nothing is created anywhere else. | `string` | n/a | yes |
| store\_prefix | Object prefix artifactweave writes under (blob.gcs.prefix). Empty writes at the bucket's root. | `string` | `""` | no |
| turbo\_replication | Turbo replication: a 15-minute recovery point objective instead of the default (99.9% of new objects within an hour), billed per GB written. Dual-regions only. | `bool` | `false` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| artifactweave\_config | The blob section of artifactweave.yaml for the bucket. It is one store: the second region is Cloud Storage's copy, not a mirror member. artifactweave authenticates with Application Default Credentials, as the app service account, which also signs download URLs. |
| bucket | The bucket's name (blob.gcs.bucket). |
| expires\_at | For the test harness: the expires-at label (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise. |
| location | The bucket's dual-region, or the multi-region its data\_locations lie in. |
| rpo | The bucket's replication: DEFAULT (99.9% of new objects in the second region within an hour) or ASYNC\_TURBO (15 minutes). |
| storage\_host | Where a downloadRedirect repository's signed URLs point: Cloud Storage's path-style endpoint. One signed URL serves the bucket in either region. |
<!-- END_TF_DOCS -->
