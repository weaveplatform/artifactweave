# azure/single-bucket: one storage account

One storage account and private container in one region, without Shared Key, for
artifactweave's `azure` blob backend. It is the simplest deployment on Azure and the baseline
the other Azure scenarios build on. Choose it when one region's copies are enough; choose
`geo-replicated` to have Azure copy the account to its paired region, or `multi-region` for a
mirror across regions under artifactweave's control.

## What it deploys

- One storage account with its container, lifecycle policy and role assignments, through
  [`modules/azure/storage`](../../modules/azure/storage/README.md). It is named after `name`
  (its letters and digits, lowercased, at most 15), a random suffix and the location's first
  three letters, within Azure's 24 characters.
- `random_string.suffix`: six characters, so the account name is globally unique.
- `azurerm_resource_group.this`: `<name>-rg` in `location`, unless `resource_group_name` names
  an existing group.

## How it behaves

- **Uploads** go to the container, under `store_prefix`, each blob named by its SHA-256. An
  upload whose digest is not declared up front is staged under `<store_prefix>_uploads/` and
  copied to its name once complete; the lifecycle policy removes what an interrupted one
  leaves there after two days.
- **Reads** stream through artifactweave from the account.
- **Redirects:** a `downloadRedirect` repository answers a download with a 302 to a user
  delegation SAS URL on the account's blob endpoint (`storage_host`), so the client fetches
  the blob from Azure directly.
- **Copies:** the account is LRS, three copies within one datacentre. Losing the region stops
  uploads and reads until it is back.

## Prerequisites

- Terraform 1.9 or later, and an Azure CLI login (`az login`) that can create a storage
  account and role assignments in the resource group: Contributor and Role Based Access
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
location         = "uksouth"
app_principal_id = "00000000-0000-0000-0000-00000000000a"

# Optional
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
for the account and container, with `prefix` when `store_prefix` is set. It uses the `default`
credential, the Azure SDK's chain, so no key or SAS is in the file, and signs download URLs
with a user delegation SAS. The example below is the rendered output for the example inputs
above, with the random suffix `0a1b2c`; the `readme_example` test fails if they differ.

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
downloads with a 302 to a user delegation SAS URL at `storage_host`, the account's blob
endpoint. The [configuration reference](../../../../../docs/operations/configuration-reference.md)
lists every key.

## Security

The account carries the [`modules/azure/storage` invariants](../../modules/azure/storage/README.md#invariants):
no public containers, HTTPS and TLS 1.2 only, Shared Key disabled so every request carries an
Entra ID identity, encryption at rest, and abandoned uploads expired. The app identity, and
each of `additional_principals`, holds Storage Blob Data Contributor on the account: it can
read, write, list and delete blobs and mint user delegation keys, and cannot change the
account's settings, keys or role assignments.

### Accepted findings

| ID | Resource (module) | Why |
|---|---|---|
| AZU-0012 | `azurerm_storage_account.blobs` (`azure/storage`) | The account takes connections from any network, because artifactweave and the clients its signed URLs serve have no fixed addresses; with Shared Key off, every request still needs an Entra ID token or a user delegation SAS. |
| AZU-0057 | `azurerm_storage_account.blobs` (`azure/storage`) | No Storage Analytics logs: the activity log records the management calls. |
| AZU-0058 | `azurerm_storage_account.blobs` (`azure/storage`) | LRS on purpose: one region's copies is what this scenario offers; `geo-replicated` and `multi-region` keep a second region. |
| AZU-0060 | `azurerm_storage_account.blobs` (`azure/storage`) | Microsoft-managed keys; a customer-managed key would add a Key Vault and its access policies to manage. |
| AZU-0061 | `azurerm_storage_account.blobs` (`azure/storage`) | No infrastructure (double) encryption: service-side encryption already applies. |

## Cost

### Steady state

Hot LRS block blobs, roughly $0.02 per GB-month in the UK regions, plus operations per
10,000 and downloads leaving Azure as internet egress. A redirected download leaves from the
account, so its egress is the account's. There is no fixed or hourly charge.

### Per test run

Well under one cent: a proof stores well under 100 MiB for minutes, and its cost is mostly
the test's downloads leaving Azure and its operations.

## Teardown

`terraform destroy` removes the account, with every blob in it, its role assignments and,
when this root created it, the resource group. A resource group passed in through
`resource_group_name` stays, and so does the app identity, which is not this scenario's.

## How this scenario is tested

- **Mocked tests** (`tests/scenario.tftest.hcl`, in `make deploy-check`) check the
  composition: `readme_example` renders the example above from the example inputs; `ci_run`
  uses the longest instance a proof produces, checking that the account name fits 24
  characters, that a given resource group is used, and the outputs reap and `test/platform`
  read; `lab_run` checks the lab's inputs; others check the resource group this root creates
  and the inputs' validation.
- **The real-cloud proof** (`platform-scenarios.yml`, on a pull request that changes this
  scenario, its module or the harness) applies an instance as the CI deployer, which also
  runs artifactweave, and runs `test/platform`: a 40 MiB upload, round trip, ranged GET and
  checksums through artifactweave, a `downloadRedirect` 302 that lands on `storage_host` and
  serves ranges, and the account refusing the URL without its signature. It then destroys the
  instance, applies and destroys another from its remote state alone, and checks that reap
  removes an expired one.
- **Harness outputs** (`harness.tf`): `expires_at`, the tag reap reads from the state.
- **The lab's conformance account** is this scenario applied with the Makefile's inputs:
  `make conformance` runs the real-Azure blob contract and probes against it as you, and
  `make github-vars` sets `CONFORMANCE_AZURE_ACCOUNT` and `CONFORMANCE_AZURE_CONTAINER` from
  the `account` and `container` outputs for `cloud-conformance.yml`, which runs them as the
  bootstrap's conformance identity. The tests write under `conformance/`, which expires
  after a day in case a run skips its cleanup.
- **Locally**, `make apply`, `up`, `smoke`, `verify` and `destroy` run the same checks
  against an instance in the lab ([running a scenario locally](../../../../ci/README.md#running-a-scenario-locally)).

| Variable | Scenario run (`ci-vars.sh`) | Operator run (`Makefile`, `deploy/ci/lab-vars.sh`) |
|---|---|---|
| `subscription_id` | `PLATFORM_AZURE_SUBSCRIPTION_ID` | `LAB_AZURE_SUBSCRIPTION` |
| `name` | `${NAME_PREFIX}${INSTANCE}`: `awscn-<instance>`, the start reap knows scenario names by | `artifactweave-acc` |
| `location` | `uksouth` | `uksouth` |
| `resource_group_name` | `PLATFORM_AZURE_RESOURCE_GROUP`, the bootstrap's scenarios group, where the deployer's rights end | none: the root creates `artifactweave-acc-rg` |
| `app_principal_id` | the deployer, from the `oid` claim of its access token | the bootstrap's conformance identity |
| `additional_principals` | none | `operator`: you, from `az ad signed-in-user show`, as a `User` |
| `store_prefix` | none | `local/`, where the local run writes |
| `expiring_prefixes` | none | `conformance/` after 1 day |
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
| location | Region of the storage account, and of the resource group this root creates. | `string` | n/a | yes |
| name | Start of every resource name. The storage account takes its letters and digits, lowercased and cut to fit 24 characters with a random suffix and the location's first three letters; the resource group this root creates is <name>-rg. | `string` | n/a | yes |
| resource\_group\_name | An existing resource group to create the account in. Null creates <name>-rg in location. | `string` | `null` | no |
| store\_prefix | Blob prefix artifactweave writes under in the container (blob.azure.prefix). Empty writes at the container's root. | `string` | `""` | no |
| subscription\_id | Subscription that holds the storage account. Nothing is created anywhere else. | `string` | n/a | yes |
| tags | Tags for the storage account, and for the resource group when this root creates it. | `map(string)` | `{}` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| account | The storage account (blob.azure.account). |
| artifactweave\_config | The blob section of artifactweave.yaml for the account. It authenticates through the Azure SDK's default chain, as the app identity, and signs download URLs with a user delegation SAS. |
| container | The container in the account (blob.azure.container). |
| expires\_at | For the test harness: the expires-at tag (epoch seconds) a scenario run sets, which reap reads from the state; empty otherwise. |
| resource\_group | The resource group holding the account. |
| storage\_host | Where a downloadRedirect repository's signed URLs point: the account's blob endpoint. |
<!-- END_TF_DOCS -->
