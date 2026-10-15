# aws/multi-region: a mirror across two S3 regions

Two regional buckets as members of one artifactweave mirror (decision
[0028](../../../../../docs/research/decisions/0028-blob-mirroring.md)): every blob is written
to both, an upload succeeds once `write_quorum` of them hold it, and reads prefer the member in
this instance's zone. It is the reference for regional resilience under artifactweave's
control: either region can be lost without losing a blob or stopping uploads.

One AWS provider serves both regions through the per-resource `region` of the AWS provider 6. Each bucket policy grants the bootstrap's `artifactweave-conformance` role, or in a scenario run the deployer, which runs artifactweave in the same job. Apply [`deploy/ci/terraform/aws`](../../../../ci/terraform/aws/README.md) first.

## What Terraform creates

- Two buckets, in `eu-west-2` and `eu-west-1` by default, each through [`modules/aws/storage`](../../modules/aws/storage/README.md): all public access blocked, ACLs off, S3-managed encryption, TLS only, abandoned upload parts deleted after two days and multipart uploads aborted after one, and a bucket policy granting the app role that bucket's objects. Names are `<name>-<suffix>-<region code>` (`euw2`, `euw1`), which keeps a scenario run's names inside S3's 63 characters.
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

Storage is billed twice, once per bucket (S3 Standard, roughly $0.024 per GB-month in these regions). artifactweave writes each copy from where it runs, so the copy to the farther region, and every repair copy, crosses regions as data transfer (roughly $0.02 per GB). Requests are billed per bucket. A scenario run stores under 50 MiB for minutes and costs a fraction of a cent.

With `write_quorum: 1` an upload waits for the nearer region only. Raise it to 2 for a copy in both regions before the upload returns, at the cost of the farther region's latency and of uploads stopping while either region is down. S3 cross-region replication is no substitute: its copies arrive within minutes, the mirror cannot see them, and a redirect could not use them.

## Use

Run it in CI, which applies, tests and destroys it in one run:

```bash
gh workflow run platform-scenarios.yml -f cloud=aws -f scenario=multi-region -f action=test
```

Or apply it yourself and point a local artifactweave at the rendered configuration:

```bash
aws login   # once
make apply          # from the repository root: make platform-apply SCENARIO=aws/multi-region
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
| [random_id.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/id) | resource |
| [aws_iam_role.conformance](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/iam_role) | data source |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| app\_role\_arn | ARN of the role artifactweave runs as, which both bucket policies grant. Empty looks up conformance\_role\_name; scenario runs pass the deployer, which runs artifactweave in the same job and cannot read the bootstrap's roles. | `string` | `""` | no |
| conformance\_role\_name | Name of the data-plane role aws/bootstrap created; both bucket policies grant it their objects. | `string` | `"artifactweave-conformance"` | no |
| name | Prefix for the bucket names; each is <name>-<suffix>-<region code>. It must start with artifactweave-, the only buckets the deployer may manage; scenario runs pass artifactweave-awscn-<instance>. | `string` | `"artifactweave-mr"` | no |
| primary\_region | Region of the first member, and this instance's zone (blob.mirror.zone): reads prefer it. | `string` | `"eu-west-2"` | no |
| scenario\_labels | Tags for both buckets. Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches. | `map(string)` | `{}` | no |
| secondary\_region | Region of the second member. | `string` | `"eu-west-1"` | no |
| store\_prefix | Key prefix artifactweave writes under in each bucket (blob.stores.<name>.s3.prefix). | `string` | `"local/"` | no |
| write\_quorum | Copies stored before an upload succeeds (blob.mirror.write\_quorum). 1 keeps uploads going with a region down; the second copy follows in the background or through repair. | `number` | `1` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| app\_role\_arn | The role both bucket policies grant: the bootstrap's conformance role, unless app\_role\_arn names another. |
| artifactweave\_config | The blob section of artifactweave.yaml: a mirror of the two buckets, preferring the primary region. Credentials come from the AWS default chain. |
| buckets | The member buckets, by region. |
| expires\_at | The expires-at tag (epoch seconds) a scenario run set, read by reap; empty outside scenario runs. |
| mirror\_members | The mirror's members in order (this instance's zone first): store name, zone, and what a presigned URL for one of its objects starts with after the scheme. test/platform checks which member a redirect lands on with it. |
| storage\_host | Host a downloadRedirect repository's signed URLs point at when the primary member signs them, as it does once it holds the blob. |
<!-- END_TF_DOCS -->
