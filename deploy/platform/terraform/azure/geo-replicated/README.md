# azure/geo-replicated

One storage account that Azure replicates to the paired region: read-access geo-zone-redundant
storage (RA-GZRS), with its primary in `uksouth` and its secondary in `ukwest` (decision
[0030](../../../../../docs/research/decisions/0030-reference-deployments.md)). It is the
shape for operators who want the cloud's own replication rather than artifactweave's
mirror. Shared Key is off, as in `azure/single-bucket`. Apply
[`deploy/ci/terraform/azure`](../../../../ci/terraform/azure/README.md) first.

## How it relates to artifactweave

artifactweave writes to and signs URLs for the primary endpoint,
`<account>.blob.core.windows.net`. Azure copies every blob to the paired region, which serves
reads at the secondary endpoint, `<account>-secondary.blob.core.windows.net` (the
`secondary_storage_host` output).

- **Invisible to artifactweave.** The secondary never appears in the mirror's replica index
  and never counts toward a mirror's write quorum (decision
  [0028](../../../../../docs/research/decisions/0028-blob-mirroring.md)). Don't add Azure
  object replication on top: its destination refuses writes with 409, so it can't be a
  mirror member
  ([research 20](../../../../../docs/research/20-reference-deployments.md#23-azure)).
- **Recovery point objective.** Geo priority replication puts the RPO for block blobs at
  15 minutes or less (research 20).
- **Reading the secondary.** A user delegation SAS signs the account, container and blob
  path, not the host. So a download URL artifactweave signed for the primary also reads the
  secondary when its host is swapped. That lets a client or proxy fall back to the secondary
  while the primary is unreachable.
- **Failover.** An account failover (`az storage account failover`) promotes the secondary,
  and the account name and artifactweave's configuration stay the same. Expect blobs written
  in the last RPO window to be missing.

## What it creates

- **A storage account** `awgeo<suffix>`: StorageV2, `RAGZRS`, Shared Key disabled, no
  public containers, TLS 1.2 and HTTPS only.
- **Replication type:** `replication_type` also takes `RAGRS` (locally redundant in the
  primary region), or `GZRS` and `GRS`, where the secondary can only be read after a
  failover.
- **A private container** `artifactweave`, and a management policy expiring
  `local/_uploads/` after 2 days and `conformance/` after a day.
- **Storage Blob Data Contributor on the account** for the bootstrap's conformance identity
  (unless `conformance_identity` is empty) and for whoever applies.
- **Its own resource group**, `artifactweave-geo-rg`, unless `resource_group_name` names
  one.

## Use

`az login` and `az account set --subscription c33dbc35-f86d-4b7c-a81e-2a33eb3f7946`
first. From this directory:

```bash
make plan
make apply
terraform output -raw artifactweave_config   # the blob section for artifactweave.yaml
make destroy
```

`make up`, `smoke` and `verify` run artifactweave locally from this scenario's configuration
([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)); the
conformance target is in `azure/single-bucket`. Every target that touches Azure first checks
the CLI's active subscription and tenant.

## Scenario runs

`platform-scenarios.yml` runs this root as the deployer
(`-f cloud=azure -f scenario=geo-replicated`), through
[`deploy/ci/scenario-ci.sh`](../../../../ci/scenario-ci.sh) and [`ci-vars.sh`](ci-vars.sh),
inside the bootstrap's scenarios resource group, with the account tagged for reap.

Besides the generic checks, [`test/platform`](../../../../../test/platform/replica_test.go)
asks artifactweave for a signed download URL and swaps in the secondary host. It retries
for up to 12 minutes until the secondary serves the same bytes. A blob not yet replicated by
then is reported, not failed. Different content fails the run.

## Cost

Cents per run: RA-GZRS storage and geo-replication bandwidth for 40 MiB, plus requests on
both endpoints. Destroy the scenario when the run is done.

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
| [azurerm_role_assignment.conformance](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/role_assignment) | resource |
| [azurerm_role_assignment.operator](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/role_assignment) | resource |
| [azurerm_storage_account.blobs](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/storage_account) | resource |
| [azurerm_storage_container.blobs](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/storage_container) | resource |
| [azurerm_storage_management_policy.cleanup](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/storage_management_policy) | resource |
| [random_string.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/string) | resource |
| [azurerm_client_config.current](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/data-sources/client_config) | data source |
| [azurerm_resource_group.existing](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/data-sources/resource_group) | data source |
| [azurerm_user_assigned_identity.conformance](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/data-sources/user_assigned_identity) | data source |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| bootstrap\_resource\_group | Resource group of azure/bootstrap, which holds the conformance identity. | `string` | `"artifactweave-bootstrap-rg"` | no |
| conformance\_identity | Name of the data-plane identity azure/bootstrap created; it gets blob rights on this account. Empty grants nothing beyond whoever applies (scenario runs). | `string` | `"artifactweave-conformance"` | no |
| container | Blob container for blob.azure.container. | `string` | `"artifactweave"` | no |
| location | Primary region of the resource group and storage account; the secondary is its Azure pair (ukwest for uksouth). RA-GZRS needs a region with availability zones. | `string` | `"uksouth"` | no |
| name | Prefix for the resource names. | `string` | `"artifactweave-geo"` | no |
| register\_resource\_providers | Register Microsoft.Storage and Microsoft.ManagedIdentity. Scenario runs pass false: the deployer has no subscription-level rights, and the bootstrap's subscription has them registered already. | `bool` | `true` | no |
| replication\_type | Geo-redundant replication: RAGZRS (zone-redundant primary, readable secondary), RAGRS (locally redundant primary, readable secondary), GZRS or GRS (secondary readable only after a failover). | `string` | `"RAGZRS"` | no |
| resource\_group\_name | An existing resource group to create the account in. Empty creates <name>-rg; scenario runs pass azure/bootstrap's scenarios group, where the deployer's rights end. | `string` | `""` | no |
| scenario\_labels | Tags for the account (and the resource group when this root creates it). Scenario runs set artifactweave-scenario, instance and expires-at (epoch seconds), which reap matches. | `map(string)` | `{}` | no |
| store\_prefix | Blob prefix artifactweave writes under (blob.azure.prefix). | `string` | `"local/"` | no |
| subscription\_id | Subscription that holds the storage account. Nothing is created anywhere else. | `string` | `"c33dbc35-f86d-4b7c-a81e-2a33eb3f7946"` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| account | Storage account for blob.azure.account. |
| artifactweave\_config | The blob section of artifactweave.yaml for this account: one store, written at the primary. It authenticates through the Azure SDK chain and signs download URLs with a user delegation SAS. |
| container | Container for blob.azure.container. |
| expires\_at | The expires-at tag (epoch seconds) a scenario run set, read by reap; empty outside scenario runs. |
| replication\_type | The account's geo-redundant replication type. |
| resource\_group | Resource group holding the account. |
| secondary\_location | The paired region that holds Azure's copy. |
| secondary\_storage\_host | The secondary blob endpoint (<account>-secondary), readable with RAGZRS or RAGRS. A user delegation SAS minted for the primary reads it too. |
| storage\_host | Host a downloadRedirect repository's signed URLs point at (the primary). |
| store\_prefix | Blob name prefix artifactweave writes under (blob.azure.prefix). |
<!-- END_TF_DOCS -->
