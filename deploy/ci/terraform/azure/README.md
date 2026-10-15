# The Azure bootstrap (`deploy/ci/terraform/azure`)

The long-lived foundation every Azure scenario stands on, in the lab subscription
`c33dbc35-f86d-4b7c-a81e-2a33eb3f7946` (decision
[0030](../../../../docs/research/decisions/0030-reference-deployments.md)). It is not a
scenario: an operator applies it once, and scenarios are created and destroyed without
touching it.

## What it holds

- **`artifactweave-bootstrap-rg`**, created by CLI with the DNS zone and imported here.
  It holds the identities, the state account and the zone.
- **`artifactweave-scenarios-rg`**, where every scenario instance is created. The
  deployer's management rights stop at its boundary. Empty, it costs nothing.
- **GitHub's trust.** Two user-assigned identities, each with one federated credential
  that matches its GitHub environment's subject exactly, in GitHub's immutable form
  (`repo:weaveplatform@336817058/artifactweave@1415179278:environment:<env>`). Azure
  allows no wildcards there.

  | Identity | Trusted subject | Rights |
  |---|---|---|
  | `artifactweave-conformance` | `cloud-conformance` | Storage Blob Data Contributor on the `single-bucket` account only (granted there) |
  | `artifactweave-deployer` | `cloud-platform` | Contributor and Storage Blob Data Contributor on the scenarios group; Role Based Access Control Administrator there, limited by condition to assigning Storage Blob Data Contributor and Reader; DNS Zone Contributor on this zone; Storage Blob Data Contributor on the state account |

- **`Microsoft.Cdn`** registered, for Front Door (`azure/cdn`). The other namespaces the
  scenarios use were already registered.
- **Remote state** in `awtfstatec33dbc35`, container `tfstate`: Entra ID only (Shared Key
  off), private, blob versioning with 30 days of deleted-blob retention. This root's own
  state is `bootstrap/azure.tfstate`, and scenario runs live under
  `scenarios/<cloud>/<scenario>/<instance>`.
- **The DNS zone** `azure.artifact.weaveplatform.run`, delegated at GoDaddy and imported.
  Terraform refuses to destroy it.
- **A budget** on the subscription, alerting its owners at 50%, 90% and 100% of
  `budget_amount`.

## Use

`az login` and `az account set --subscription c33dbc35-f86d-4b7c-a81e-2a33eb3f7946` first.
Every target checks the active subscription and tenant before touching Azure. From this
directory, or with `make ci-<target> CLOUD=azure` from the repository
root:

```bash
make plan
make apply FIRST_RUN=1   # first run only: on local state, because the state account doesn't exist yet
make migrate-state   # once: moves the state into the account just created
make github-vars     # creates both GitHub environments and sets the repository variables
```

Only with `FIRST_RUN=1` does `make init` write a git-ignored `local_override.tf`, replacing
the azurerm backend with local state while the state account is missing; it removes it once the
account exists. Without `FIRST_RUN`, a state probe that fails stops the run, so an expired
login can never plan this bootstrap from empty local state.
The backend reaches the account with your Entra ID login (`use_azuread_auth`). The
Storage Blob Data Contributor grant can take a few minutes to apply after the first
apply, so a `migrate-state` straight afterwards may need a retry.

`make destroy` refuses: CI trust, state and the delegated zone live here. To retire a
part, remove it from the configuration and apply, deliberately.

## Cost

Nothing here has a fixed or hourly charge. Identities, federated credentials, role
assignments, resource groups, provider registrations and budgets are free. The state
account holds kilobytes, billed per GB with no account fee. The DNS zone is billed per
zone per month (about $0.50) and stays, because its delegation does.

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.7 |
| azurerm | ~> 5.9 |

### Providers

| Name | Version |
| ---- | ------- |
| azurerm | 5.9.0 |

### Modules

No modules.

### Resources

| Name | Type |
| ---- | ---- |
| [azurerm_consumption_budget_subscription.lab](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/consumption_budget_subscription) | resource |
| [azurerm_dns_zone.azure](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/dns_zone) | resource |
| [azurerm_federated_identity_credential.conformance](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/federated_identity_credential) | resource |
| [azurerm_federated_identity_credential.deployer](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/federated_identity_credential) | resource |
| [azurerm_resource_group.bootstrap](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/resource_group) | resource |
| [azurerm_resource_group.scenarios](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/resource_group) | resource |
| [azurerm_resource_provider_registration.cdn](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/resource_provider_registration) | resource |
| [azurerm_role_assignment.deployer_blob_data](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/role_assignment) | resource |
| [azurerm_role_assignment.deployer_contributor](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/role_assignment) | resource |
| [azurerm_role_assignment.deployer_dns](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/role_assignment) | resource |
| [azurerm_role_assignment.deployer_grants_blob_roles](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/role_assignment) | resource |
| [azurerm_role_assignment.deployer_state](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/role_assignment) | resource |
| [azurerm_role_assignment.operator_state](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/role_assignment) | resource |
| [azurerm_storage_account.state](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/storage_account) | resource |
| [azurerm_storage_container.state](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/storage_container) | resource |
| [azurerm_user_assigned_identity.conformance](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/user_assigned_identity) | resource |
| [azurerm_user_assigned_identity.deployer](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/user_assigned_identity) | resource |
| [azurerm_client_config.current](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/data-sources/client_config) | data source |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| budget\_amount | Monthly budget for the subscription, in its billing currency. Alerts go to the subscription's owners at 50%, 90% and 100%. | `number` | `20` | no |
| budget\_start | First month the budget covers, as the first day of a month in RFC 3339. | `string` | `"2026-10-01T00:00:00Z"` | no |
| conformance\_environment | GitHub environment whose jobs act as the data-plane identity (cloud-conformance.yml). | `string` | `"cloud-conformance"` | no |
| github\_subject\_prefix | The OIDC subject prefix of this repository. It uses GitHub's immutable subject, which names the owner and repository with their IDs; read it with `gh api repos/{owner}/{repo}/actions/oidc/customization/sub --jq .sub_claim_prefix`. | `string` | `"repo:weaveplatform@336817058/artifactweave@1415179278"` | no |
| location | Region of the resource groups, identities and state account. | `string` | `"uksouth"` | no |
| platform\_environment | GitHub environment whose jobs act as the deployer identity (platform-auth-check.yml, and the scenario workflow in #129). | `string` | `"cloud-platform"` | no |
| resource\_group | The bootstrap's resource group, created by CLI with the DNS zone; imported here. | `string` | `"artifactweave-bootstrap-rg"` | no |
| scenarios\_resource\_group | The resource group every scenario instance is created in. The deployer's rights stop at its boundary. | `string` | `"artifactweave-scenarios-rg"` | no |
| subscription\_id | The lab subscription every Azure scenario runs in. Nothing is created anywhere else. | `string` | `"c33dbc35-f86d-4b7c-a81e-2a33eb3f7946"` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| conformance\_client\_id | The data-plane identity's client ID (CONFORMANCE\_AZURE\_CLIENT\_ID); azure/single-bucket grants it its account. |
| conformance\_identity | The data-plane identity's name, which azure/single-bucket looks up. |
| deployer\_client\_id | The deployer identity's client ID (PLATFORM\_AZURE\_CLIENT\_ID). |
| dns\_name\_servers | Name servers for the NS records at GoDaddy (azure.artifact under weaveplatform.run). |
| resource\_group | The bootstrap's resource group, holding the identities, state account and DNS zone. |
| scenarios\_resource\_group | The resource group scenarios are created in (PLATFORM\_AZURE\_RESOURCE\_GROUP). |
| state\_container | Container inside the state account. |
| state\_storage\_account | Remote state account for this bootstrap and every scenario run (PLATFORM\_AZURE\_STATE\_ACCOUNT). |
| subscription\_id | The lab subscription, for azure/login (CONFORMANCE\_AZURE\_SUBSCRIPTION\_ID and PLATFORM\_AZURE\_SUBSCRIPTION\_ID). |
| tenant\_id | The tenant, for azure/login (CONFORMANCE\_AZURE\_TENANT\_ID and PLATFORM\_AZURE\_TENANT\_ID). |
<!-- END_TF_DOCS -->
