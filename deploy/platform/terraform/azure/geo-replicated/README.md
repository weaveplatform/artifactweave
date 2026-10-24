# azure/geo-replicated: one account Azure copies to its paired region

One storage account that Azure replicates to the paired region, read-access geo-zone-redundant
(RA-GZRS) by default: three zones in the primary region, a copy in the pair (`ukwest` for
`uksouth`), and read access to that copy. Choose it to have the cloud's own replication rather
than artifactweave's: the copy is Azure's, invisible to artifactweave, and an account failover
promotes it without changing artifactweave's configuration. Choose `multi-region` instead for a
mirror under artifactweave's control that keeps uploading with a region down, or
`single-bucket` when one region is enough.

## What it deploys

- One storage account with its container, lifecycle policy and role assignments, through
  [`modules/azure/storage`](../../modules/azure/storage/README.md), with `replication_type`
  set. It is named after `name` (its letters and digits, lowercased, at most 15), a random
  suffix and the location's first three letters, within Azure's 24 characters.
- `random_string.suffix`: six characters, so the account name is globally unique.
- `azurerm_resource_group.this`: `<name>-rg` in `location`, unless `resource_group_name` names
  an existing group.

## How it behaves

- **Uploads and redirects** go to the primary endpoint, `storage_host`, as in
  `single-bucket`: artifactweave writes there and signs user delegation SAS URLs for it.
- **Replication:** Azure copies every blob to the paired region, which serves reads at the
  secondary endpoint, `secondary_storage_host` (`<account>-secondary`). Geo priority
  replication puts the recovery point objective for block blobs at 15 minutes or less
  ([research 20](../../../../../docs/research/20-reference-deployments.md#23-azure)).
- **Invisible to artifactweave:** the copy never appears in a mirror's replica index and never
  counts toward a write quorum (decision
  [0028](../../../../../docs/research/decisions/0028-blob-mirroring.md)). Azure object
  replication cannot stand in for it either: its destination refuses writes with 409, so it
  cannot be a mirror member.
- **Reading the copy:** a user delegation SAS signs the account, container and blob path, not
  the host, so a URL artifactweave signed for the primary also reads the secondary when its
  host is swapped. A client or proxy can fall back to it while the primary is unreachable.
- **Failover:** an account failover (`az storage account failover`) promotes the secondary; the
  account name and artifactweave's configuration stay the same. Blobs written within the last
  recovery point window may be missing.
- `replication_type` also takes `RAGRS` (locally redundant primary, readable secondary), and
  `GZRS` or `GRS`, whose copy is readable only after a failover, so `secondary_storage_host`
  is null.

## Prerequisites

- Terraform 1.9 or later, and an Azure CLI login (`az login`) that can create a storage
  account and role assignments in the resource group: Contributor and Role Based Access
  Control Administrator on it, or Owner. Creating the group needs Contributor on the
  subscription.
- `Microsoft.Storage` registered in the subscription. The provider registers nothing
  (`resource_provider_registrations = "none"`); `az provider register --namespace
  Microsoft.Storage` does it once.
- A `location` with a paired region, and with availability zones for `RAGZRS` and `GZRS`.
- The identity artifactweave runs as, created elsewhere (a managed identity, or a service
  principal), whose object ID is `app_principal_id`. This scenario grants it blob data rights;
  it does not create it.

### Getting the code

Copy this directory together with `modules/azure/` and `.terraform.lock.hcl`, keeping the
`../../modules` layout, or point the module at the repository:

```hcl
source = "git::https://github.com/weaveplatform/artifactweave.git//deploy/platform/terraform/modules/azure/storage?ref=v0.1.1" # x-release-please-version
```

## Inputs to set

```hcl
# terraform.tfvars
subscription_id  = "00000000-0000-0000-0000-000000000000"
name             = "artifactweave-example"
location         = "uksouth"
app_principal_id = "00000000-0000-0000-0000-00000000000a"

# Optional
# replication_type      = "RAGRS"
# resource_group_name   = "an-existing-group"
# additional_principals = { operator = { principal_id = "00000000-0000-0000-0000-00000000000b", principal_type = "User" } }
# store_prefix          = "artifacts/"
# expiring_prefixes     = { "scratch/" = 1 }
# tags                  = { team = "platform" }
```

```bash
terraform init
terraform apply
```

## The artifactweave configuration

`artifactweave_config` renders the `blob` section of `artifactweave.yaml`: one `azure` store
for the account's primary endpoint, with `prefix` when `store_prefix` is set. Nothing in it
names the secondary, which Azure keeps. It uses the `default` credential, the Azure SDK's
chain, so no key or SAS is in the file, and signs download URLs with a user delegation SAS.
The example below is the rendered output for the example inputs above, with the random suffix
`0a1b2c`; the `readme_example` test fails if they differ.

<!-- BEGIN_CONFIG_EXAMPLE -->
```yaml
blob:
  backend: azure
  azure:
    account: artifactweaveex0a1b2cuks
    container: artifactweave
    credential: default
```
<!-- END_CONFIG_EXAMPLE -->

### Running artifactweave with it

```bash
terraform output -raw artifactweave_config > artifactweave.yaml
export ARTIFACTWEAVE_MASTER_KEY="$(openssl rand -base64 32)"   # keep it: it seals stored secrets
export ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD='choose one'      # read on first start only
artifactweave serve --config artifactweave.yaml
```

Run it as `app_principal_id`, a managed identity on its VM or container, or a workload
identity, so the Azure SDK's default chain finds it. A `downloadRedirect` repository answers
downloads with a 302 to a user delegation SAS URL at `storage_host`, the primary endpoint. The
[configuration reference](../../../../../docs/operations/configuration-reference.md) lists
every key.

## Security

The account carries the [`modules/azure/storage` invariants](../../modules/azure/storage/README.md#invariants):
no public containers, HTTPS and TLS 1.2 only, Shared Key disabled so every request carries an
Entra ID identity, encryption at rest, and abandoned uploads expired. The secondary endpoint
takes the same identities and SAS as the primary. The app identity, and each of
`additional_principals`, holds Storage Blob Data Contributor on the account: it can read,
write, list and delete blobs and mint user delegation keys, and cannot change the account's
settings, keys or role assignments, or start a failover.

### Accepted findings

| ID | Resource (module) | Why |
|---|---|---|
| AZU-0012 | `azurerm_storage_account.blobs` (`azure/storage`) | The account takes connections from any network, because artifactweave and the clients its signed URLs serve have no fixed addresses; with Shared Key off, every request still needs an Entra ID token or a user delegation SAS. |
| AZU-0057 | `azurerm_storage_account.blobs` (`azure/storage`) | No Storage Analytics logs: the activity log records the management calls. |
| AZU-0058 | `azurerm_storage_account.blobs` (`azure/storage`) | The module's default is LRS; this scenario always passes a geo-redundant `replication_type`, which its validation enforces. |
| AZU-0060 | `azurerm_storage_account.blobs` (`azure/storage`) | Microsoft-managed keys; a customer-managed key would add a Key Vault and its access policies to manage. |
| AZU-0061 | `azurerm_storage_account.blobs` (`azure/storage`) | No infrastructure (double) encryption: service-side encryption already applies. |

## Cost

### Steady state

RA-GZRS block blobs cost roughly two and a half times LRS, about $0.05 per GB-month in the UK
regions, and Azure bills each GB it copies to the pair as geo-replication data transfer
(roughly $0.02 per GB). Operations are billed per 10,000, reads from the secondary at the
read-access rate, and downloads leaving Azure as internet egress. There is no fixed or hourly
charge.

### Per test run

About one cent: a proof stores well under 100 MiB for minutes, and replicates it once.

## Teardown

`terraform destroy` removes the account, with every blob in it and Azure's copy, its role
assignments and, when this root created it, the resource group. A resource group passed in
through `resource_group_name` stays, and so does the app identity, which is not this
scenario's.

## How this scenario is tested

- **Mocked tests** (`tests/scenario.tftest.hcl`, in `make deploy-check`) check the
  composition: `readme_example` renders the example above from the example inputs; `ci_run`
  uses the longest instance a proof produces, checking that the account name fits 24
  characters, that a given resource group is used, and the outputs reap and `test/platform`
  read; others check that the default keeps a readable copy, that `secondary_storage_host` is
  null without one, that LRS is refused, the resource group this root creates and the inputs'
  validation.
- **The real-cloud proof** (`platform-scenarios.yml`, on a pull request that changes this
  scenario, its module or `test/platform/replica_test.go`) applies an instance as the CI
  deployer, which also runs artifactweave, and runs `test/platform`: the generic checks, as
  in `single-bucket`, then a download URL artifactweave signed for the primary, with its host
  swapped for `secondary_storage_host`, retried for up to 12 minutes until the secondary
  serves the same bytes. A blob not replicated by then is reported, not failed; different
  content fails the run. It then destroys the instance, applies and destroys another from its
  remote state alone, and checks that reap removes an expired one.
- **Harness outputs** (`harness.tf`): `expires_at`, the tag reap reads from the state.
- **Locally**, `make apply`, `up`, `smoke`, `verify` and `destroy` run the same checks
  against an instance in the lab ([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)).

| Variable | Scenario run (`ci-vars.sh`) | Operator run (`Makefile`, `deploy/ci/lab-vars.sh`) |
|---|---|---|
| `subscription_id` | `PLATFORM_AZURE_SUBSCRIPTION_ID` | `LAB_AZURE_SUBSCRIPTION` |
| `name` | `${NAME_PREFIX}${INSTANCE}`: `awscn-<instance>`, the start reap knows scenario names by | `artifactweave-geo` |
| `location` | `uksouth` | `uksouth` |
| `resource_group_name` | `PLATFORM_AZURE_RESOURCE_GROUP`, the bootstrap's scenarios group, where the deployer's rights end | none: the root creates `artifactweave-geo-rg` |
| `app_principal_id` | the deployer, from the `oid` claim of its access token | the bootstrap's conformance identity |
| `additional_principals` | none | `operator`: you, from `az ad signed-in-user show`, as a `User` |
| `tags` | `$SCENARIO_LABELS`: `artifactweave-scenario`, `instance`, `expires-at` | none |

## Reference

Generated by terraform-docs (`make deploy-docs`).

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.9 |
| azurerm | ~> 5.9 |
| random | ~> 3.9 |

### Providers

| Name | Version |
| ---- | ------- |
| azurerm | 5.9.0 |
| random | 3.9.1 |

### Modules

| Name | Source | Version |
| ---- | ------ | ------- |
| storage | ../../modules/azure/storage | n/a |

### Resources

| Name | Type |
| ---- | ---- |
| [azurerm_resource_group.this](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/resource_group) | resource |
| [random_string.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/string) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| additional\_principals | Further principals that get Storage Blob Data Contributor on the account, by a static name: an operator who runs artifactweave or mints user delegation SAS from a workstation, for one. principal\_type (User, Group or ServicePrincipal) is optional. | ```map(object({ principal_id = string principal_type = optional(string) }))``` | `{}` | no |
| app\_principal\_id | Object ID of the identity artifactweave runs as (a managed identity, or a service principal). It gets Storage Blob Data Contributor on the account. | `string` | n/a | yes |
| container | Blob container in the account (blob.azure.container). | `string` | `"artifactweave"` | no |
| expiring\_prefixes | Blob prefixes, from the container's root, whose blobs expire after the given number of days, for example { "scratch/" = 1 }. | `map(number)` | `{}` | no |
| location | Primary region of the storage account, and of the resource group this root creates. Azure keeps its copy in the region's pair (ukwest for uksouth); the zone-redundant types need a region with availability zones. | `string` | n/a | yes |
| name | Start of every resource name. The storage account takes its letters and digits, lowercased and cut to fit 24 characters with a random suffix and the location's first three letters; the resource group this root creates is <name>-rg. | `string` | n/a | yes |
| replication\_type | Geo-redundant replication: RAGZRS (zone-redundant primary, readable secondary), RAGRS (locally redundant primary, readable secondary), GZRS or GRS (secondary readable only after a failover). | `string` | `"RAGZRS"` | no |
| resource\_group\_name | An existing resource group to create the account in. Null creates <name>-rg in location. | `string` | `null` | no |
| store\_prefix | Blob prefix artifactweave writes under in the container (blob.azure.prefix). Empty writes at the container's root. | `string` | `""` | no |
| subscription\_id | Subscription that holds the storage account. Nothing is created anywhere else. | `string` | n/a | yes |
| tags | Tags for the storage account, and for the resource group when this root creates it. | `map(string)` | `{}` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| account | The storage account (blob.azure.account). |
| artifactweave\_config | The blob section of artifactweave.yaml for the account: one store, written at the primary. It authenticates through the Azure SDK's default chain, as the app identity, and signs download URLs with a user delegation SAS. |
| container | The container in the account (blob.azure.container). |
| expires\_at | For the test harness: the expires-at tag (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise. |
| resource\_group | The resource group holding the account. |
| secondary\_storage\_host | The paired region's read endpoint (<account>-secondary), which a user delegation SAS minted for the primary also reads; null with GZRS or GRS, whose copy is readable only after a failover. |
| storage\_host | Where a downloadRedirect repository's signed URLs point: the account's primary blob endpoint. |
<!-- END_TF_DOCS -->
