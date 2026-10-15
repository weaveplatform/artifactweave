# azure/multi-region: a mirror across two Azure regions

Two regional storage accounts as members of one artifactweave mirror (decision
[0028](../../../../../docs/research/decisions/0028-blob-mirroring.md)): every blob is written
to both, an upload succeeds once `write_quorum` of them hold it, and reads prefer the member in
this instance's zone. It is the reference for regional resilience under artifactweave's
control: either region can be lost without losing a blob or stopping uploads.

Shared Key is off on both accounts. They are granted to whoever applies and to the bootstrap's `artifactweave-conformance` identity; in a scenario run the deployer applies them in the bootstrap's scenarios resource group and runs artifactweave, which signs download URLs with a user delegation SAS. Apply [`azure/bootstrap`](../bootstrap/README.md) first.

## What Terraform creates

- Two storage accounts, in `uksouth` and `ukwest` by default, each through [`modules/azure/storage`](../../modules/azure/storage/README.md): LRS, Entra ID only, a private container, abandoned temporary blobs deleted after two days, and Storage Blob Data Contributor for you and the conformance identity. Locally the root creates `<name>-rg` in the primary region; a scenario run uses the bootstrap's scenarios resource group.
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

Storage is billed twice, once per account (LRS hot storage, roughly $0.02 per GB-month in UK regions). artifactweave writes each copy from where it runs, so the copy to the farther region, and every repair copy, crosses regions as data transfer (roughly $0.02 per GB within Europe). Transactions are billed per account. A scenario run stores under 50 MiB for minutes and costs a fraction of a cent.

With `write_quorum: 1` an upload waits for the nearer region only. Raise it to 2 for a copy in both regions before the upload returns, at the cost of the farther region's latency and of uploads stopping while either region is down. Azure object replication cannot stand in for the mirror: it refuses writes to its destination container (409), which would break the mirror's own copy.

## Use

Run it in CI, which applies, tests and destroys it in one run:

```bash
gh workflow run platform-scenarios.yml -f cloud=azure -f scenario=multi-region -f action=test
```

Or apply it yourself and point a local artifactweave at the rendered configuration:

```bash
az login && az account set --subscription <subscription>   # once
make apply          # from the repository root: make platform-apply SCENARIO=azure/multi-region
terraform output -raw artifactweave_config > blob.yaml
make destroy        # removes both members, their objects included
```

There is no local container or conformance target here; `single-bucket` has those.

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.6 |
| azurerm | ~> 5.9 |
| random | ~> 3.9 |

### Providers

| Name | Version |
| ---- | ------- |
| azurerm | 5.9.0 |
| random | 3.9.1 |

### Resources

| Name | Type |
| ---- | ---- |
| [azurerm_resource_group.this](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/resource_group) | resource |
| [random_string.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/string) | resource |
| [azurerm_client_config.current](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/data-sources/client_config) | data source |
| [azurerm_resource_group.existing](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/data-sources/resource_group) | data source |
| [azurerm_user_assigned_identity.conformance](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/data-sources/user_assigned_identity) | data source |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| bootstrap\_resource\_group | Resource group of azure/bootstrap, which holds the conformance identity. | `string` | `"artifactweave-bootstrap-rg"` | no |
| conformance\_identity | Name of the data-plane identity azure/bootstrap created; it gets blob rights on both accounts. Empty grants nothing beyond whoever applies (scenario runs, where the deployer applies and runs artifactweave). | `string` | `"artifactweave-conformance"` | no |
| container | Blob container in each account (blob.stores.<name>.azure.container). | `string` | `"artifactweave"` | no |
| name | Prefix for the resource group this root creates when resource\_group\_name is empty. | `string` | `"artifactweave-mr"` | no |
| primary\_location | Region of the first member, and this instance's zone (blob.mirror.zone): reads prefer it. | `string` | `"uksouth"` | no |
| register\_resource\_providers | Register Microsoft.Storage and Microsoft.ManagedIdentity. Scenario runs pass false: the deployer has no subscription-level rights, and the bootstrap's subscription has them registered already. | `bool` | `true` | no |
| resource\_group\_name | An existing resource group to create the accounts in. Empty creates <name>-rg; scenario runs pass azure/bootstrap's scenarios group, where the deployer's rights end. | `string` | `""` | no |
| scenario\_labels | Tags for both accounts (and the resource group when this root creates it). Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches. | `map(string)` | `{}` | no |
| secondary\_location | Region of the second member. | `string` | `"ukwest"` | no |
| store\_prefix | Blob prefix artifactweave writes under in each account (blob.stores.<name>.azure.prefix). | `string` | `"local/"` | no |
| subscription\_id | Subscription that holds the storage accounts. Nothing is created anywhere else. | `string` | `"c33dbc35-f86d-4b7c-a81e-2a33eb3f7946"` | no |
| write\_quorum | Copies stored before an upload succeeds (blob.mirror.write\_quorum). 1 keeps uploads going with a region down; the second copy follows in the background or through repair. | `number` | `1` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| accounts | The member storage accounts, by region. |
| artifactweave\_config | The blob section of artifactweave.yaml: a mirror of the two accounts, preferring the primary region. Each store authenticates through the Azure SDK chain and signs download URLs with a user delegation SAS. |
| expires\_at | The expires-at tag (epoch seconds) a scenario run set, read by reap; empty outside scenario runs. |
| mirror\_members | The mirror's members in order (this instance's zone first): store name, zone, and what a signed URL for one of its blobs starts with after the scheme. test/platform checks which member a redirect lands on with it. |
| resource\_group | Resource group holding the accounts. |
| storage\_host | Host a downloadRedirect repository's signed URLs point at when the primary member signs them, as it does once it holds the blob. |
| subscription\_id | Subscription holding the accounts. |
<!-- END_TF_DOCS -->
