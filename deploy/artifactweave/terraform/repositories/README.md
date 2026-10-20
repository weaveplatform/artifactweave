# repositories: local repositories, with properties on a repository and one file

A local generic repository with a set of properties on the repository itself and another on
one file in it; local npm, Go, Maven, Gradle, PyPI, NuGet, Helm, Helm OCI, Terraform module,
Terraform provider and Terraform backend repositories; a GPG key pair for the Terraform
provider repository; and two virtual repositories, managed with the `jfrog/artifactory`
provider. Use it as the starting point for repository configuration kept in Terraform. See
[Managing artifactweave with Terraform](../../README.md) for the providers and how to
authenticate them.

## What it configures

- `artifactory_local_generic_repository.this`: the local generic repository `repository_key`.
- `data.artifactory_repositories.generic`: the local generic repositories, as the repositories
  list reports them. The list spells each package type as Artifactory does, so the
  `local_generic_repositories` output shows `Generic` for the repository above.
- `artifactory_item_properties.repository`: `repository_properties` on the generic repository
  itself. The resource has no `item_path`, so the provider sets them on the repository root,
  and `is_recursive` is false, so they do not reach the files in it.
- `artifactory_item_properties.this`: `properties` on the file at `item_path` in that
  repository, when `with_properties` is true.
- `artifactory_local_npm_repository.this` and `artifactory_local_go_repository.this`: local npm
  and Go modules repositories, `npm_repository_key` and `go_repository_key`, on the
  `npm-default` and `go-default` layouts.
- `artifactory_local_maven_repository.this`: a release repository, `maven_repository_key`, on
  `maven-2-default`: releases accepted, snapshots refused, server-generated checksums, POM
  consistency checks on.
- `artifactory_local_gradle_repository.this`: a snapshot repository, `gradle_repository_key`, on
  `gradle-default`: releases refused, non-unique snapshots with at most five kept per version,
  client checksums, POM consistency checks suppressed.
- `artifactory_local_pypi_repository.this` and
  `artifactory_local_terraformbackend_repository.this`: a local PyPI repository,
  `pypi_repository_key`, and a Terraform state backend, `terraform_backend_repository_key`,
  both on `simple-default`.
- `artifactory_local_nuget_repository.this`: a NuGet feed, `nuget_repository_key`, on
  `nuget-default`, that asks for credentials even to read (`force_nuget_authentication`) and
  keeps at most ten snapshots of a package.
- `artifactory_local_helm_repository.this`: a Helm repository, `helm_repository_key`, on
  `simple-default`, that refuses a chart whose name and version another path already holds
  and one whose file name does not match its `Chart.yaml`. The provider refuses to change
  either flag once it is set, so turning one off means replacing the repository.
- `artifactory_local_helmoci_repository.this`: a Helm OCI repository, `helmoci_repository_key`,
  that keeps at most ten tags per chart and up to three overwritten tags by digest
  (`tag_retention`, `dockerTagRetention` in the repository's JSON).
- `artifactory_local_terraform_module_repository.this`: a Terraform module registry,
  `terraform_module_repository_key`, on `terraform-module-default`.
- `artifactory_local_terraform_provider_repository.this` and
  `artifactory_keypair.terraform_provider_signing`: a Terraform provider registry,
  `terraform_provider_repository_key`, on `terraform-provider-default`, and the GPG key pair
  `gpg_keypair_name` it signs with. Provider 12.11.14 has no `primary_keypair_ref` on this
  resource, so you associate the pair with the repository yourself (see
  [Applying it](#applying-it)).
- `artifactory_virtual_npm_repository.this`: the virtual npm repository
  `npm_virtual_repository_key` over the local npm repository, which also receives what is
  deployed through it.
- `artifactory_virtual_maven_repository.empty`: the virtual Maven repository
  `maven_virtual_repository_key`, with no members yet. A virtual repository's members must
  have its package type, unless it is generic, so add Maven, Gradle, Ivy or SBT repositories
  to it.

## Prerequisites

- A running artifactweave (or Artifactory) and its base URL, without `/artifactory`.
- An admin access token: the providers authenticate only with a Bearer token.
- A GPG key pair without a passphrase for the Terraform provider repository, for example one
  that `gpg --quick-gen-key '<name> <email>' rsa3072 sign never` makes with an empty
  passphrase.
- Terraform 1.6 or later, which installs `jfrog/artifactory` 12.11.14 as pinned in
  `versions.tf` and `.terraform.lock.hcl`.

## Inputs to set

```hcl
url                               = "https://artifacts.example.com"
repository_key                    = "generic-local"
npm_repository_key                = "npm-local"
go_repository_key                 = "go-local"
maven_repository_key              = "maven-releases-local"
gradle_repository_key             = "gradle-snapshots-local"
pypi_repository_key               = "pypi-local"
nuget_repository_key              = "nuget-local"
helm_repository_key               = "helm-local"
helmoci_repository_key            = "helmoci-local"
terraform_module_repository_key   = "terraform-modules-local"
terraform_provider_repository_key = "terraform-providers-local"
terraform_backend_repository_key  = "terraform-backend-local"
gpg_keypair_name                  = "terraform-providers-gpg"
npm_virtual_repository_key        = "npm"
maven_virtual_repository_key      = "maven"
item_path                         = "fixture.txt"
properties = {
  team = ["platform"]
  tier = ["gold", "silver"]
}
repository_properties = {
  owner = ["platform"]
  stage = ["test", "release"]
}
```

Pass the token and the GPG key pair through the environment rather than a file:

```sh
export TF_VAR_access_token=…
export TF_VAR_gpg_private_key="$(gpg --armor --export-secret-keys <fingerprint>)"
export TF_VAR_gpg_public_key="$(gpg --armor --export <fingerprint>)"
```

## Applying it

The provider cannot deploy content, so the file the properties go on must exist before they
are applied. Nor can it associate the GPG key pair with the Terraform provider repository:
provider 12.11.14 has no `primary_keypair_ref` on
`artifactory_local_terraform_provider_repository`, so the repositories API sets
`primaryKeyPairRef` between the two applies:

```sh
terraform init
terraform apply -var with_properties=false   # everything but the file's properties
curl -fsS -H "Authorization: Bearer $TF_VAR_access_token" -X PUT --data-binary @fixture.txt \
  "$TF_VAR_url/artifactory/generic-local/fixture.txt"
curl -fsS -H "Authorization: Bearer $TF_VAR_access_token" -X POST \
  -H 'Content-Type: application/json' -d '{"primaryKeyPairRef":"terraform-providers-gpg"}' \
  "$TF_VAR_url/artifactory/api/repositories/terraform-providers-local"
terraform apply                               # the file's properties
```

A second `terraform plan` shows no changes: the provider reads back exactly what it wrote,
including each repository's layout, the Maven and Gradle repositories' release, snapshot,
checksum and POM settings, the NuGet, Helm and Helm OCI settings, the key pair's type and
public key, and the virtual repositories' members, an empty list included. The provider
neither sends nor reads `primaryKeyPairRef`, so later applies keep the association.

## Security

The access token and the GPG private key are sensitive variables, so Terraform keeps them out
of plans and logs. They are still stored in the state, so keep the state private. The
configuration grants no permissions: who can read or deploy to the repository is decided by
the permissions you manage elsewhere, for example with the
[identity](../identity/README.md) reference.

## Teardown

`terraform destroy` removes the properties, the key pair and the repositories, with every
file in them, including the one you deployed by hand. The key pair and the Terraform
provider repository may go in either order: deleting a key pair that a repository still
names clears that repository's `primaryKeyPairRef`.

## How this scenario is tested

The gated `terraform` job runs `test/terraform/run.sh` against a fresh `-cover` artifactweave
(see [docs/testing/ci.md](../../../../docs/testing/ci.md#terraform-scenarios)). It passes
`repository_key = tf-generic-local`, `item_path = fixture.txt`,
`gpg_keypair_name = tf-terraform-providers-gpg` and a fresh GPG key pair from
`tools/gpgkey`, applies with `with_properties = false`, deploys `fixture.txt`,
sets the Terraform provider repository's `primaryKeyPairRef` through the repositories API,
applies again, checks that `local_generic_repositories` lists `tf-generic-local` as `Generic`
and that the repository still names the key pair, requires an empty plan and destroys
everything. The empty plan also proves the repository's own properties read back as
written. The other repositories keep their default keys, and the empty plan is what proves
their layouts, the Maven, Gradle, NuGet, Helm and Helm OCI settings, the key pair and the
virtual repositories' members round-trip.

## Reference

Generated by terraform-docs (`make deploy-docs`).

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.6 |
| artifactory | 12.11.14 |

### Providers

| Name | Version |
| ---- | ------- |
| artifactory | 12.11.14 |

### Modules

No modules.

### Resources

| Name | Type |
| ---- | ---- |
| [artifactory_item_properties.repository](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/item_properties) | resource |
| [artifactory_item_properties.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/item_properties) | resource |
| [artifactory_keypair.terraform_provider_signing](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/keypair) | resource |
| [artifactory_local_generic_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_generic_repository) | resource |
| [artifactory_local_go_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_go_repository) | resource |
| [artifactory_local_gradle_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_gradle_repository) | resource |
| [artifactory_local_helm_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_helm_repository) | resource |
| [artifactory_local_helmoci_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_helmoci_repository) | resource |
| [artifactory_local_maven_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_maven_repository) | resource |
| [artifactory_local_npm_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_npm_repository) | resource |
| [artifactory_local_nuget_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_nuget_repository) | resource |
| [artifactory_local_pypi_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_pypi_repository) | resource |
| [artifactory_local_terraform_module_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_terraform_module_repository) | resource |
| [artifactory_local_terraform_provider_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_terraform_provider_repository) | resource |
| [artifactory_local_terraformbackend_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_terraformbackend_repository) | resource |
| [artifactory_virtual_maven_repository.empty](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_maven_repository) | resource |
| [artifactory_virtual_npm_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_npm_repository) | resource |
| [artifactory_repositories.generic](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/data-sources/repositories) | data source |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| access\_token | Admin access token, sent as a Bearer token. | `string` | n/a | yes |
| go\_repository\_key | Key of the local Go modules repository. | `string` | `"go-local"` | no |
| gpg\_keypair\_name | Name and alias of the GPG key pair the Terraform provider repository signs with. | `string` | `"terraform-providers-gpg"` | no |
| gpg\_private\_key | ASCII-armored GPG private key of that key pair, without a passphrase. | `string` | n/a | yes |
| gpg\_public\_key | ASCII-armored GPG public key matching gpg\_private\_key. | `string` | n/a | yes |
| gradle\_repository\_key | Key of the local Gradle snapshot repository. | `string` | `"gradle-snapshots-local"` | no |
| helm\_repository\_key | Key of the local Helm repository. | `string` | `"helm-local"` | no |
| helmoci\_repository\_key | Key of the local Helm OCI repository. | `string` | `"helmoci-local"` | no |
| item\_path | Path, inside the repository, of the file the properties are set on. The file must exist before the properties are applied: the provider cannot deploy content. | `string` | `"fixture.txt"` | no |
| maven\_repository\_key | Key of the local Maven release repository. | `string` | `"maven-releases-local"` | no |
| maven\_virtual\_repository\_key | Key of the virtual Maven repository, which has no members. | `string` | `"maven"` | no |
| npm\_repository\_key | Key of the local npm repository. | `string` | `"npm-local"` | no |
| npm\_virtual\_repository\_key | Key of the virtual npm repository over the local npm repository. | `string` | `"npm"` | no |
| nuget\_repository\_key | Key of the local NuGet repository. | `string` | `"nuget-local"` | no |
| properties | Properties to set on item\_path, each a key with its values. | `map(list(string))` | ```{ "team": [ "platform" ], "tier": [ "gold", "silver" ] }``` | no |
| pypi\_repository\_key | Key of the local PyPI repository. | `string` | `"pypi-local"` | no |
| repository\_key | Key of the local generic repository. | `string` | `"generic-local"` | no |
| repository\_properties | Properties to set on the generic repository itself, each a key with its values. | `map(list(string))` | ```{ "owner": [ "platform" ], "stage": [ "test", "release" ] }``` | no |
| terraform\_backend\_repository\_key | Key of the local Terraform backend repository. | `string` | `"terraform-backend-local"` | no |
| terraform\_module\_repository\_key | Key of the local Terraform module registry repository. | `string` | `"terraform-modules-local"` | no |
| terraform\_provider\_repository\_key | Key of the local Terraform provider registry repository. | `string` | `"terraform-providers-local"` | no |
| url | Base URL of artifactweave, without /artifactory, for example https://artifacts.example.com. | `string` | n/a | yes |
| with\_properties | Whether to set the properties on item\_path. Apply with false first, deploy the file, then apply with true. | `bool` | `true` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| gpg\_keypair\_name | Name of the GPG key pair to associate with the Terraform provider repository as its primaryKeyPairRef. |
| item\_path | Path of the file the properties are set on, or null while with\_properties is false. |
| local\_generic\_repositories | Package type of each local generic repository, by key, as the repositories data source lists them. |
| package\_repository\_keys | Keys of the local package repositories (npm, Go, Maven, Gradle, PyPI, NuGet, Helm, Helm OCI, Terraform module, Terraform provider and Terraform backend), by format. |
| repository\_key | Key of the local generic repository. |
| repository\_properties | Properties set on the generic repository itself. |
| virtual\_repository\_keys | Keys of the virtual npm repository and of the virtual Maven repository, which has no members. |
<!-- END_TF_DOCS -->
