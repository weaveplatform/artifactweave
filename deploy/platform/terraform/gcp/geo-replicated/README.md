# gcp/geo-replicated

One GCS bucket that Google replicates across two regions: a dual-region, `EUR4` (Finland
and the Netherlands) by default (decision
[0030](../../../../../docs/research/decisions/0030-reference-deployments.md)). It is the
shape for operators who want the cloud's own replication rather than artifactweave's
mirror. Apply [`gcp/bootstrap`](../bootstrap/README.md) first.

## How it relates to artifactweave

A dual-region bucket is one bucket. artifactweave writes, reads and signs URLs against it as
it would any bucket, and Google keeps a copy in each region.

- **Invisible to artifactweave.** The second region never appears in the mirror's replica
  index and never counts toward a mirror's write quorum (decision
  [0028](../../../../../docs/research/decisions/0028-blob-mirroring.md)). Research 18
  suggests treating a dual-region bucket as *one* mirror member if you also run a mirror.
- **Recovery point objective.** By default, 99.9% of newly written objects replicate within
  an hour and all of them within 12 hours. Turbo replication (`turbo_replication = true`,
  dual-regions only) targets 15 minutes
  ([research 20](../../../../../docs/research/20-reference-deployments.md#22-google-cloud)).
- **No failover step.** Cloud Storage is strongly consistent and the bucket name doesn't
  change, so the same configuration and the same signed URLs keep working if a region goes
  down. Objects written within the RPO window may be unavailable until it returns.

## What it creates

- **A private dual-region bucket** `artifactweave-geo-<suffix>`. It has public access
  prevention enforced and uniform bucket-level access, with soft delete off: the backend
  deletes its upload parts, and soft delete would bill each one for the retention period in
  both regions. Destroy removes its objects with it.
- **Placement:** `location` names a predefined dual-region. To build a configurable
  dual-region instead, set `location = "EU"` with `data_locations = ["europe-west1",
  "europe-west4"]`.
- **Lifecycle rules:** `local/_uploads/` parts expire after 2 days and `conformance/` after
  a day.
- **`roles/storage.objectAdmin` on the bucket** for the app service account: the
  bootstrap's `artifactweave-conformance`, unless `app_service_account` names another.

## Use

`gcloud auth application-default login` first. From this directory:

```bash
make plan
make apply
terraform output -raw artifactweave_config   # the blob section for artifactweave.yaml
make destroy
```

There is no local container or conformance target; `gcp/single-bucket` has those.

## Scenario runs

`platform-scenarios.yml` runs this root as the deployer
(`-f cloud=gcp -f scenario=geo-replicated`), through
[`../../scenario-ci.sh`](../../scenario-ci.sh) and [`ci-vars.sh`](ci-vars.sh), with the
bucket named `awscn-<instance>-<suffix>`, granted to the deployer and labelled for reap.

GCS exposes no per-object replication status. So beyond the generic checks,
[`test/platform`](../../../../../test/platform/replica_test.go) reads the real bucket's
placement and fails unless it is a dual-region. The generic checks' signed URL already
proves one URL serves the bucket.

## Cost

Cents per run. Dual-region storage costs more per GB than a single region, and Google
charges inter-region replication per GB written; turbo replication adds another per-GB
charge. Destroy the scenario when the run is done.

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.6 |
| google | ~> 8.6 |
| random | ~> 3.9 |

### Providers

| Name | Version |
| ---- | ------- |
| google | 8.6.0 |
| random | 3.9.1 |

### Resources

| Name | Type |
| ---- | ---- |
| [google_storage_bucket.blobs](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/storage_bucket) | resource |
| [google_storage_bucket_iam_member.app](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/storage_bucket_iam_member) | resource |
| [random_id.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/id) | resource |
| [google_service_account.conformance](https://registry.terraform.io/providers/hashicorp/google/latest/docs/data-sources/service_account) | data source |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_service\_account | Email of the service account artifactweave runs as and that gets object rights on the bucket. Empty uses conformance\_service\_account; scenario runs pass the deployer. | `string` | `""` | no |
| conformance\_service\_account | Account ID of the data-plane service account gcp/bootstrap created; it gets object rights on this bucket. | `string` | `"artifactweave-conformance"` | no |
| data\_locations | The two regions of a configurable dual-region, for example ["europe-west1", "europe-west4"] with location EU. Empty uses the predefined dual-region in location. | `list(string)` | `[]` | no |
| location | A predefined dual-region (EUR4: Finland and the Netherlands), or a multi-region such as EU together with data\_locations for a configurable dual-region. | `string` | `"EUR4"` | no |
| name | Prefix for the bucket name. | `string` | `"artifactweave-geo"` | no |
| project\_id | Project that holds the bucket (the bootstrap's lab project). | `string` | `"project-0c0b2d7a-9ad7-4221-9a8"` | no |
| scenario\_labels | Labels for the bucket. Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches. | `map(string)` | `{}` | no |
| store\_prefix | Object prefix artifactweave writes under (blob.gcs.prefix). | `string` | `"local/"` | no |
| turbo\_replication | Turbo replication: a 15-minute recovery point objective instead of the default (99.9% within an hour), billed per GB written. Dual-regions only. | `bool` | `false` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| artifactweave\_config | The blob section of artifactweave.yaml for this bucket. It is one store: the second region is Google's copy, not a mirror member. |
| bucket | Bucket for blob.gcs.bucket. |
| expires\_at | The expires-at label (epoch seconds) a scenario run set, read by reap; empty outside scenario runs. |
| location | The bucket's dual-region (or multi-region, with data\_locations). |
| rpo | DEFAULT (99.9% of new objects replicated within an hour) or ASYNC\_TURBO (15 minutes). |
| service\_account | The service account artifactweave runs as (the bootstrap's conformance account unless app\_service\_account names another), for blob.gcs.google\_access\_id. |
| storage\_host | Host a downloadRedirect repository's signed URLs point at. One signed URL serves every location of the bucket. |
| store\_prefix | Object prefix artifactweave writes under (blob.gcs.prefix). |
<!-- END_TF_DOCS -->
