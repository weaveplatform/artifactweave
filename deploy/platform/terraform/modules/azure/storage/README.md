# modules/azure/storage

One storage account and private container a scenario stores blobs in, without Shared Key,
with Storage Blob Data Contributor for each principal given. `azure/multi-region` calls it
once per region. The module holds no provider block, so the caller's `azurerm` provider and
subscription apply (decision [0030](../../../../../../docs/research/decisions/0030-reference-deployments.md)).

The account is LRS, Entra ID only, TLS 1.2 and HTTPS only, with no public containers, and a
management policy deletes abandoned `<container>/<store_prefix>_uploads/` blobs after two
days. `tests/security.tftest.hcl` checks those invariants on a mocked plan, so the scenarios
that call the module check only how they compose it.

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.6 |
| azurerm | ~> 5.9 |

### Providers

| Name | Version |
| ---- | ------- |
| azurerm | 5.9.0 |

### Resources

| Name | Type |
| ---- | ---- |
| [azurerm_role_assignment.app](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/role_assignment) | resource |
| [azurerm_storage_account.blobs](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/storage_account) | resource |
| [azurerm_storage_container.blobs](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/storage_container) | resource |
| [azurerm_storage_management_policy.cleanup](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/storage_management_policy) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| account\_name | Storage account name: 3 to 24 lowercase letters and digits, globally unique. | `string` | n/a | yes |
| container | Blob container for blob.azure.container. | `string` | n/a | yes |
| location | Region of the account, for example uksouth. | `string` | n/a | yes |
| principals | Principals that get Storage Blob Data Contributor on the account, by a static name (operator, conformance) so the plan does not wait for their IDs. | `map(string)` | n/a | yes |
| resource\_group\_name | Resource group to create the account in. | `string` | n/a | yes |
| store\_prefix | Blob prefix artifactweave writes under (blob.azure.prefix); its \_uploads/ blobs expire after two days. | `string` | n/a | yes |
| tags | Tags for the account. Scenario runs set artifactweave-scenario, instance and expires-at, which reap matches. | `map(string)` | `{}` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| account | The storage account's name, for blob.azure.account. |
| container | The container's name, for blob.azure.container. |
| host | The account's blob endpoint host, where its signed URLs point. |
| signed\_url\_prefix | What a signed URL for a blob in this container starts with after the scheme. |
<!-- END_TF_DOCS -->
