# gcp/single-bucket: one private Cloud Storage bucket

One regional Cloud Storage bucket as artifactweave's `gcs` store, with object rights for the
service account artifactweave runs as. It is the simplest path: choose it when one region is
enough, since Cloud Storage already keeps a regional bucket's data redundant across the
region's zones. Choose `multi-region` for a mirror across regions under artifactweave's
control, or `geo-replicated` to have Cloud Storage replicate a dual-region bucket on its own.

## What it deploys

- One bucket through [`modules/gcp/storage`](../../modules/gcp/storage/README.md), named
  `<name>-<suffix>`, in `location`, with object rights for `app_service_account`.
- `random_id.suffix`: eight hex characters in the bucket name, so it is globally unique.

## How it behaves

- **Uploads** write each blob to the bucket under `store_prefix`. A large upload is composed
  from parts under `<store_prefix>_uploads/`, which the backend deletes once composed; a
  lifecycle rule removes any that an interrupted upload leaves, after two days.
- **Reads** stream from the bucket. A `downloadRedirect` repository answers a download with a
  302 to a signed URL on `storage.googleapis.com`, which the app service account signs
  through the IAM Credentials API, so the client fetches the blob from Cloud Storage
  directly.
- **Losing a zone** stops nothing: a regional bucket's data is spread across the region's
  zones. Losing the whole region stops uploads and downloads until it is back.

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
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/gcp/storage?ref=v0.0.0" # x-release-please-version
```

## Inputs to set

```hcl
# terraform.tfvars
project_id          = "example-project"
name                = "artifactweave-example"
location            = "europe-west2"
app_service_account = "artifactweave@example-project.iam.gserviceaccount.com"

# Optional
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
the bucket, which signs download URLs as `app_service_account` (`google_access_id`) and names
no key: Application Default Credentials supply the identity. `prefix` appears only when
`store_prefix` is set. The example below is the rendered output for the example inputs above,
with the random suffix `0a1b2c3d`; the `readme_example` test fails if they differ.

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
Application Default Credentials find it. A `downloadRedirect` repository answers downloads
with a 302 to a signed URL on `storage_host`, `storage.googleapis.com`, path-style. The
[configuration reference](../../../../../docs/operations/configuration-reference.md) lists
every key.

## Security

The bucket carries the [`modules/gcp/storage` invariants](../../modules/gcp/storage/README.md#invariants):
public access prevention enforced, uniform bucket-level access, Google-managed encryption,
and abandoned uploads expired. The app service account can create, read, list and delete
objects in this bucket, and nothing else: its grant is on the bucket, not the project, and it
cannot change the bucket's settings or IAM policy.

### Accepted findings

| ID | Resource (module) | Why |
|---|---|---|
| GCP-0007 | `google_storage_bucket_iam_member.app` (`gcp/storage`) | `roles/storage.objectAdmin` is bound to one bucket, not the project, and the backend needs all of it, deleting the parts it composes included. |
| GCP-0066 | `google_storage_bucket.blobs` (`gcp/storage`) | Google-managed encryption keys; a customer-managed key would add a KMS key ring and its grants to manage. |
| GCP-0077 | `google_storage_bucket.blobs` (`gcp/storage`) | No access-log bucket: Cloud Audit Logs record the administrative calls. |
| GCP-0078 | `google_storage_bucket.blobs` (`gcp/storage`) | No versioning: blobs are content-addressed and never rewritten, and versioning would keep every deleted upload part. |

## Cost

### Steady state

Standard storage in one region (roughly $0.020 to $0.023 per GB-month in the European
regions), operations billed per thousand, and downloads leaving Google billed as internet
egress. There is no fixed or hourly charge.

### Per test run

About a cent: a proof stores well under 100 MiB for minutes, and its cost is mostly the
test's downloads leaving Google and its operations.

## Teardown

`terraform destroy` removes the bucket and its grant. It fails while the bucket holds objects
unless `force_destroy` is true, which deletes them with the bucket. Nothing outlives the
scenario: the app service account is not this scenario's.

## How this scenario is tested

- **Mocked tests** (`tests/scenario.tftest.hcl`, in `make deploy-check`) check the
  composition: `readme_example` renders the example above from the example inputs; `ci_run`
  uses the longest instance a proof produces, checking that the bucket name fits 63
  characters and the outputs reap and `test/platform` read; `operator_run` applies the lab
  environment's inputs; others check the inputs' validation.
- **The real-cloud proof** (`platform-scenarios.yml`, on a pull request that changes this
  scenario, its module or the harness that drives every scenario) applies an instance as the
  CI deployer, which also runs artifactweave, and runs `test/platform`'s generic checks: a
  40 MiB deploy, a round trip, ranged reads, checksums, and a `downloadRedirect` 302 to a
  signed URL on `storage_host` that the bucket refuses without its signature. It then
  destroys the instance, applies and destroys another from its remote state alone, and
  checks that reap removes an expired one.
- **Harness outputs** (`harness.tf`): `expires_at`, the label reap reads from the state, and
  `service_account`, which `make conformance` passes to the real-GCS tests.
- **Locally**, this is the lab's GCS acceptance environment (`make gcp-<target>` from the
  repository root). `make apply`, `up`, `smoke`, `verify` and `destroy` run the checks above
  against it ([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally));
  `make conformance` runs the real-GCS blob contract and probes (`pkg/blob/gcs`) against it
  as the conformance service account; `make github-vars` sets `CONFORMANCE_GCS_BUCKET` for
  `cloud-conformance.yml`. The environment keeps the name, prefix and expiry it had before
  this root composed the module, and `moved.tf` moves its state, so a plan shows moves only.

| Variable | Scenario run (`ci-vars.sh`) | Operator run (`Makefile`, `deploy/ci/lab-vars.sh`) |
|---|---|---|
| `project_id` | `PLATFORM_GCP_PROJECT` | `LAB_GCP_PROJECT` |
| `name` | `${NAME_PREFIX}${INSTANCE}`: `awscn-<instance>`, the start reap knows scenario names by | `artifactweave-acc` |
| `location` | `europe-west2` | `europe-west2` |
| `app_service_account` | `PLATFORM_GCP_SERVICE_ACCOUNT`, the deployer | the bootstrap's conformance service account |
| `store_prefix` | none: the bucket's root | `local/` |
| `expiring_prefixes` | none | `conformance/` after one day, what an interrupted `make conformance` leaves |
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
| expiring\_prefixes | Object prefixes, from the bucket's root, whose objects expire after the given number of days, for example { "scratch/" = 1 }. | `map(number)` | `{}` | no |
| force\_destroy | Let terraform destroy delete the bucket with every object still in it. Off, a destroy fails while the bucket holds anything. | `bool` | `false` | no |
| labels | Labels for the bucket. | `map(string)` | `{}` | no |
| location | Region of the bucket, for example europe-west2. | `string` | n/a | yes |
| name | Start of the bucket's name. The bucket is <name>-<8 hex>, which must fit Cloud Storage's 63 characters. | `string` | n/a | yes |
| project\_id | Project that holds the bucket. Nothing is created anywhere else. | `string` | n/a | yes |
| store\_prefix | Object prefix artifactweave writes under (blob.gcs.prefix). Empty writes at the bucket's root. | `string` | `""` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| artifactweave\_config | The blob section of artifactweave.yaml for the bucket. artifactweave authenticates with Application Default Credentials, as the app service account, which also signs download URLs. |
| bucket | The bucket's name (blob.gcs.bucket). |
| expires\_at | For the test harness: the expires-at label (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise. |
| service\_account | For the test harness: the app service account, which make conformance passes to the real-GCS tests as ARTIFACTWEAVE\_TEST\_GCS\_ACCESS\_ID. |
| storage\_host | Where a downloadRedirect repository's signed URLs point: Cloud Storage's path-style endpoint. |
<!-- END_TF_DOCS -->
