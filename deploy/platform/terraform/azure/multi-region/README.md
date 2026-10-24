# azure/multi-region: a mirror across Azure regions

Storage accounts in two or more regions as members of one artifactweave mirror (decision
[0028](../../../../../docs/research/decisions/0028-blob-mirroring.md)). Every blob is written
to each account, an upload succeeds once `write_quorum` of them hold it, and reads prefer the
account in this instance's zone. Choose it for regional resilience under artifactweave's
control: a region can be lost without losing a blob or stopping uploads, and a redirect only
ever points at a copy that exists. Choose `geo-replicated` instead to have Azure replicate one
account to its paired region, or `single-bucket` when one region is enough.

## What it deploys

- One storage account per entry of `locations`, each with its container, lifecycle policy and
  role assignments, through [`modules/azure/storage`](../../modules/azure/storage/README.md).
  Each is named after `name` (its letters and digits, lowercased, at most 15), a random suffix
  and the location's first three letters, within Azure's 24 characters.
- `random_string.suffix`: six characters per location, so the account names are globally
  unique and each keeps its name when the order of `locations` changes.
- `azurerm_resource_group.this`: `<name>-rg` in the first location, unless
  `resource_group_name` names an existing group.

Each account is LRS and nothing replicates between them. GRS copies are invisible to the
mirror and never count toward its quorum, and object replication refuses writes to its
destination, which would break the mirror's own copy.

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

- Terraform 1.9 or later, and an Azure CLI login (`az login`) that can create storage
  accounts and role assignments in the resource group: Contributor and Role Based Access
  Control Administrator on it, or Owner. Creating the group needs Contributor on the
  subscription.
- `Microsoft.Storage` registered in the subscription. The provider registers nothing
  (`resource_provider_registrations = "none"`); `az provider register --namespace
  Microsoft.Storage` does it once.
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
locations        = ["uksouth", "ukwest"]
app_principal_id = "00000000-0000-0000-0000-00000000000a"

# Optional
# resource_group_name   = "an-existing-group"
# additional_principals = { operator = { principal_id = "00000000-0000-0000-0000-00000000000b", principal_type = "User" } }
# store_prefix          = "artifacts/"
# write_quorum          = 2
# expiring_prefixes     = { "scratch/" = 1 }
# tags                  = { team = "platform" }
```

```bash
terraform init
terraform apply
```

## The artifactweave configuration

`artifactweave_config` renders the `blob` section of `artifactweave.yaml`: one `azure` store
per account, named after its location, with its location as its `zone`, and a mirror over
them whose `zone` is the first location. Each store uses the `default` credential, the Azure
SDK's chain, so no key or SAS is in the file, and signs download URLs with a user delegation
SAS. The example below is the rendered output for the example inputs above, with the random
suffixes `0a1b2c` and `3d4e5f`; the `readme_example` test fails if they differ.

<!-- BEGIN_CONFIG_EXAMPLE -->
```yaml
blob:
  backend: mirror
  stores:
    azure_uksouth:
      backend: azure
      zone: uksouth
      azure:
        account: artifactweaveex0a1b2cuks
        container: artifactweave
        credential: default
    azure_ukwest:
      backend: azure
      zone: ukwest
      azure:
        account: artifactweaveex3d4e5fukw
        container: artifactweave
        credential: default
  mirror:
    members:
      - azure_uksouth
      - azure_ukwest
    zone: uksouth
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

Run it as `app_principal_id`, a managed identity on its VM or container, or a workload
identity, so the Azure SDK's default chain finds it; set `blob.mirror.zone` on each instance to
the location it runs in. A `downloadRedirect` repository answers downloads with a 302 to a
user delegation SAS URL on the member that holds the blob, preferring the zone's:
`storage_host` is the first member's blob endpoint. The
[configuration reference](../../../../../docs/operations/configuration-reference.md) lists
every key.

## Security

Each account carries the [`modules/azure/storage` invariants](../../modules/azure/storage/README.md#invariants):
no public containers, HTTPS and TLS 1.2 only, Shared Key disabled so every request carries an
Entra ID identity, encryption at rest, and abandoned uploads expired. The app identity, and
each of `additional_principals`, holds Storage Blob Data Contributor on each account: it can
read, write, list and delete blobs and mint user delegation keys, and cannot change an
account's settings, keys or role assignments.

### Accepted findings

| ID | Resource (module) | Why |
|---|---|---|
| AZU-0012 | `azurerm_storage_account.blobs` (`azure/storage`) | The account takes connections from any network, because artifactweave and the clients its signed URLs serve have no fixed addresses; with Shared Key off, every request still needs an Entra ID token or a user delegation SAS. |
| AZU-0057 | `azurerm_storage_account.blobs` (`azure/storage`) | No Storage Analytics logs: the activity log records the management calls. |
| AZU-0058 | `azurerm_storage_account.blobs` (`azure/storage`) | LRS on purpose: each member keeps one region's copy, and the mirror keeps the others. |
| AZU-0060 | `azurerm_storage_account.blobs` (`azure/storage`) | Microsoft-managed keys; a customer-managed key would add a Key Vault and its access policies to manage. |
| AZU-0061 | `azurerm_storage_account.blobs` (`azure/storage`) | No infrastructure (double) encryption: service-side encryption already applies. |

## Cost

### Steady state

Storage is billed once per account: two locations store everything twice (hot LRS block
blobs, roughly $0.02 per GB-month in the UK regions). artifactweave writes each copy from
where it runs, so the copy to a farther region, and every repair copy, is inter-region data
transfer (roughly $0.02 per GB within Europe). Operations are billed per account, and
downloads leaving Azure as internet egress. There is no fixed or hourly charge.

### Per test run

About one cent: a proof stores well under 100 MiB for minutes, and its cost is mostly the
test's downloads leaving Azure and its operations.

## Teardown

`terraform destroy` removes the accounts, with every blob in them, their role assignments and,
when this root created it, the resource group. A resource group passed in through
`resource_group_name` stays, and so does the app identity, which is not this scenario's.

## How this scenario is tested

- **Mocked tests** (`tests/scenario.tftest.hcl`, in `make deploy-check`) check the
  composition: `readme_example` renders the example above from the example inputs; `ci_run`
  uses the longest instance a proof produces, checking that account names fit 24 characters,
  that a given resource group is used, and the outputs reap and `test/platform` read; others
  check the resource group this root creates and the inputs' validation.
- **The real-cloud proof** (`platform-scenarios.yml`, on a pull request that changes this
  scenario or its module) applies an instance as the CI deployer, which also runs
  artifactweave, and runs `test/platform`: both members hold each blob, a redirect prefers
  the zone's member, an upload and every read succeed with that member drained, and repair
  copies the new blob back once it is enabled again. It then destroys the instance, applies
  and destroys another from its remote state alone, and checks that reap removes an expired
  one.
- **Harness outputs** (`harness.tf`): `expires_at`, the tag reap reads from the state, and
  `mirror_members`, the members `test/platform` checks redirects against.
- **Locally**, `make apply`, `up`, `smoke`, `verify` and `destroy` run the same checks
  against an instance in the lab ([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)).

| Variable | Scenario run (`ci-vars.sh`) | Operator run (`Makefile`, `deploy/ci/lab-vars.sh`) |
|---|---|---|
| `subscription_id` | `PLATFORM_AZURE_SUBSCRIPTION_ID` | `LAB_AZURE_SUBSCRIPTION` |
| `name` | `${NAME_PREFIX}${INSTANCE}`: `awscn-<instance>`, the start reap knows scenario names by | `artifactweave-mr` |
| `locations` | `uksouth`, then `ukwest` | `uksouth`, then `ukwest` |
| `resource_group_name` | `PLATFORM_AZURE_RESOURCE_GROUP`, the bootstrap's scenarios group, where the deployer's rights end | none: the root creates `artifactweave-mr-rg` |
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
| member | ../../modules/azure/storage | n/a |

### Resources

| Name | Type |
| ---- | ---- |
| [azurerm_resource_group.this](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/resource_group) | resource |
| [random_string.suffix](https://registry.terraform.io/providers/hashicorp/random/latest/docs/resources/string) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| additional\_principals | Further principals that get Storage Blob Data Contributor on every account, by a static name: an operator who runs artifactweave or mints user delegation SAS from a workstation, for one. principal\_type (User, Group or ServicePrincipal) is optional. | ```map(object({ principal_id = string principal_type = optional(string) }))``` | `{}` | no |
| app\_principal\_id | Object ID of the identity artifactweave runs as (a managed identity, or a service principal). It gets Storage Blob Data Contributor on every account. | `string` | n/a | yes |
| container | Blob container in each account (blob.stores.<name>.azure.container). | `string` | `"artifactweave"` | no |
| expiring\_prefixes | Blob prefixes, from each container's root, whose blobs expire after the given number of days, for example { "scratch/" = 1 }. | `map(number)` | `{}` | no |
| locations | Regions of the mirror's members, one storage account each, in order. The first is this instance's zone (blob.mirror.zone): reads prefer it, and the resource group this root creates is there. | `list(string)` | n/a | yes |
| name | Start of every resource name. Storage accounts take its letters and digits, lowercased and cut to fit 24 characters with a random suffix and the location's first three letters; the resource group this root creates is <name>-rg. | `string` | n/a | yes |
| resource\_group\_name | An existing resource group to create the accounts in. Null creates <name>-rg in the first location. | `string` | `null` | no |
| store\_prefix | Blob prefix artifactweave writes under in each container (blob.stores.<name>.azure.prefix). Empty writes at the container's root. | `string` | `""` | no |
| subscription\_id | Subscription that holds the storage accounts. Nothing is created anywhere else. | `string` | n/a | yes |
| tags | Tags for every storage account, and for the resource group when this root creates it. | `map(string)` | `{}` | no |
| write\_quorum | Copies stored before an upload succeeds (blob.mirror.write\_quorum). 1 keeps uploads going with a region down; the other copies follow in the background or through repair. | `number` | `1` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| accounts | The member storage accounts, by location. |
| artifactweave\_config | The blob section of artifactweave.yaml: a mirror of the accounts, preferring the first location. Each store authenticates through the Azure SDK's default chain, as the app identity, and signs download URLs with a user delegation SAS. |
| expires\_at | For the test harness: the expires-at tag (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise. |
| mirror\_members | For the test harness: the mirror's members in order (this instance's zone first), each with its store name, zone, and what a signed URL for one of its blobs starts with after the scheme. test/platform checks which member a redirect lands on with it. |
| resource\_group | The resource group holding the accounts. |
| storage\_host | Where a downloadRedirect repository's signed URLs point: the first member's blob endpoint, which signs once it holds the blob. |
<!-- END_TF_DOCS -->
