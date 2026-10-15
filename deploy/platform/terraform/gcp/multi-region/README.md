# gcp/multi-region: a mirror across two GCS regions

Two regional buckets as members of one artifactweave mirror (decision
[0028](../../../../../docs/research/decisions/0028-blob-mirroring.md)): every blob is written
to both, an upload succeeds once `write_quorum` of them hold it, and reads prefer the member in
this instance's zone. It is the reference for regional resilience under artifactweave's
control: either region can be lost without losing a blob or stopping uploads.

The buckets are granted to the bootstrap's `artifactweave-conformance` service account, or in a scenario run to the deployer, which signs download URLs for both through IAM `signBlob`. Apply [`deploy/ci/terraform/gcp`](../../../../ci/terraform/gcp/README.md) first.

## What Terraform creates

- Two buckets, in `europe-west2` and `europe-west1` by default, each through [`modules/gcp/storage`](../../modules/gcp/storage/README.md): private, single region, soft delete off, abandoned upload parts deleted after two days, and `roles/storage.objectAdmin` for the service account artifactweave runs as.
- The rendered `artifactweave_config` output: `blob.backend: mirror` over the two stores,
  each with its region as `zone`, `blob.mirror.zone` set to the primary region and
  `write_quorum: 1`.

Neither member replicates on its own. A cloud's own copies are invisible to the mirror and
never count toward its quorum (decision 0028); [`geo-replicated`](../../../README.md#catalogue)
is the scenario that shows the cloud's replication instead.

## How it behaves

- **Uploads** go to both members in parallel and succeed once one holds the blob. The other
  copy finishes within `blob.mirror.member_timeout` (30 s), or repair copies it later.
- **Reads and redirects** prefer the member in this instance's zone, and fall back to the
  other when a member fails. A redirect only ever signs on a member that holds the blob.
- **Draining** a member (`write_enabled: false`) stops new copies on it while reads go on;
  re-enabling it lets repair copy what it missed.

[`test/platform`](../../../../../test/platform) shows each of these on the real cloud: both
members hold the blob, the redirect prefers the zone's member, an upload and every read
succeed with that member drained, and repair copies the new file back once it is re-enabled.

## Cost and latency

Storage is billed twice, once per region (Standard storage, roughly $0.02 per GB-month in these regions). artifactweave writes each copy from where it runs, so the copy to the farther region, and every repair copy, crosses regions as network egress (roughly $0.02 per GB between European regions). Requests are billed per bucket. A scenario run stores under 50 MiB for minutes and costs a fraction of a cent.

With `write_quorum: 1` an upload waits for the nearer region only. Raise it to 2 for a copy in both regions before the upload returns, at the cost of the farther region's latency and of uploads stopping while either region is down.

## Use

Run it in CI, which applies, tests and destroys it in one run:

```bash
gh workflow run platform-scenarios.yml -f cloud=gcp -f scenario=multi-region -f action=test
```

Or apply it yourself and point a local artifactweave at the rendered configuration:

```bash
gcloud auth application-default login   # once
make apply          # from the repository root: make platform-apply SCENARIO=gcp/multi-region
terraform output -raw artifactweave_config > blob.yaml
make destroy        # removes both members, their objects included
```

`make up`, `smoke` and `verify` run artifactweave locally from this scenario's configuration
([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)); the
conformance target is in `single-bucket`.

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
| [random_id.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/id) | resource |
| [google_service_account.conformance](https://registry.terraform.io/providers/hashicorp/google/latest/docs/data-sources/service_account) | data source |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_service\_account | Email of the service account artifactweave runs as and that gets object rights on both buckets. Empty uses conformance\_service\_account; scenario runs pass the deployer. | `string` | `""` | no |
| conformance\_service\_account | Account ID of the data-plane service account gcp/bootstrap created; it gets object rights on both buckets. | `string` | `"artifactweave-conformance"` | no |
| name | Prefix for the bucket names; each is <name>-<suffix>-<region>. | `string` | `"artifactweave-mr"` | no |
| primary\_region | Region of the first member, and this instance's zone (blob.mirror.zone): reads prefer it. | `string` | `"europe-west2"` | no |
| project\_id | Project that holds the buckets (the bootstrap's lab project). | `string` | `"project-0c0b2d7a-9ad7-4221-9a8"` | no |
| scenario\_labels | Labels for both buckets. Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches. | `map(string)` | `{}` | no |
| secondary\_region | Region of the second member. | `string` | `"europe-west1"` | no |
| store\_prefix | Object prefix artifactweave writes under in each bucket (blob.stores.<name>.gcs.prefix). | `string` | `"local/"` | no |
| write\_quorum | Copies stored before an upload succeeds (blob.mirror.write\_quorum). 1 keeps uploads going with a region down; the second copy follows in the background or through repair. | `number` | `1` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| artifactweave\_config | The blob section of artifactweave.yaml: a mirror of the two buckets, preferring the primary region. |
| buckets | The member buckets, by region. |
| expires\_at | The expires-at label (epoch seconds) a scenario run set, read by reap; empty outside scenario runs. |
| mirror\_members | The mirror's members in order (this instance's zone first): store name, zone, and what a signed URL for one of its objects starts with after the scheme. test/platform checks which member a redirect lands on with it. |
| service\_account | The service account artifactweave runs as (the bootstrap's conformance account unless app\_service\_account names another), which signs download URLs. |
| storage\_host | Host a downloadRedirect repository's signed URLs point at, whichever member signs them. |
<!-- END_TF_DOCS -->
