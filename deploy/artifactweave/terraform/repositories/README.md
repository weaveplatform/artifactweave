# repositories: local repositories, with properties on a repository and one file

A local generic repository with a set of properties on the repository itself and another on
one file in it, local npm, Go, Maven and Gradle repositories, and two virtual repositories,
managed with the `jfrog/artifactory` provider. Use it as the starting point for repository configuration kept
in Terraform. See [Managing artifactweave with Terraform](../../README.md) for the providers
and how to authenticate them.

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
- Terraform 1.6 or later, which installs `jfrog/artifactory` 12.11.14 as pinned in
  `versions.tf` and `.terraform.lock.hcl`.

## Inputs to set

```hcl
url                          = "https://artifacts.example.com"
repository_key               = "generic-local"
npm_repository_key           = "npm-local"
go_repository_key            = "go-local"
maven_repository_key         = "maven-releases-local"
gradle_repository_key        = "gradle-snapshots-local"
npm_virtual_repository_key   = "npm"
maven_virtual_repository_key = "maven"
item_path                    = "fixture.txt"
properties = {
  team = ["platform"]
  tier = ["gold", "silver"]
}
repository_properties = {
  owner = ["platform"]
  stage = ["test", "release"]
}
```

Pass the token through the environment rather than a file: `export TF_VAR_access_token=…`.

## Applying it

The provider cannot deploy content, so the file the properties go on must exist before they
are applied. That takes two applies:

```sh
terraform init
terraform apply -var with_properties=false   # the repository and its own properties
curl -fsS -H "Authorization: Bearer $TF_VAR_access_token" -X PUT --data-binary @fixture.txt \
  "$TF_VAR_url/artifactory/generic-local/fixture.txt"
terraform apply                               # the file's properties
```

A second `terraform plan` shows no changes: the provider reads back exactly what it wrote,
including each repository's layout, the Maven and Gradle repositories' release, snapshot,
checksum and POM settings, and the virtual repositories' members, an empty list included.

## Security

The access token is a sensitive variable, so Terraform keeps it out of plans and logs. It is
still stored in the state, so keep the state private. The configuration grants no
permissions: who can read or deploy to the repository is decided by the permissions you
manage elsewhere, for example with the [identity](../identity/README.md) reference.

## Teardown

`terraform destroy` removes the properties and the repositories, with every file in them,
including the one you deployed by hand.

## How this scenario is tested

The gated `terraform` job runs `test/terraform/run.sh` against a fresh `-cover` artifactweave
(see [docs/testing/ci.md](../../../../docs/testing/ci.md#terraform-scenarios)). It passes
`repository_key = tf-generic-local` and `item_path = fixture.txt`, applies with
`with_properties = false`, deploys `fixture.txt`, applies again, checks that
`local_generic_repositories` lists `tf-generic-local` as `Generic`, requires an empty plan and
destroys everything. The empty plan also proves the repository's own properties read back as
written. The npm, Go, Maven, Gradle and virtual repositories keep their default keys, and
the empty plan is what proves their layouts, the Maven and Gradle settings and the virtual
repositories' members round-trip.

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
| [artifactory_local_generic_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_generic_repository) | resource |
| [artifactory_local_go_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_go_repository) | resource |
| [artifactory_local_gradle_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_gradle_repository) | resource |
| [artifactory_local_maven_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_maven_repository) | resource |
| [artifactory_local_npm_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_npm_repository) | resource |
| [artifactory_virtual_maven_repository.empty](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_maven_repository) | resource |
| [artifactory_virtual_npm_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_npm_repository) | resource |
| [artifactory_repositories.generic](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/data-sources/repositories) | data source |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| access\_token | Admin access token, sent as a Bearer token. | `string` | n/a | yes |
| go\_repository\_key | Key of the local Go modules repository. | `string` | `"go-local"` | no |
| gradle\_repository\_key | Key of the local Gradle snapshot repository. | `string` | `"gradle-snapshots-local"` | no |
| item\_path | Path, inside the repository, of the file the properties are set on. The file must exist before the properties are applied: the provider cannot deploy content. | `string` | `"fixture.txt"` | no |
| maven\_repository\_key | Key of the local Maven release repository. | `string` | `"maven-releases-local"` | no |
| maven\_virtual\_repository\_key | Key of the virtual Maven repository, which has no members. | `string` | `"maven"` | no |
| npm\_repository\_key | Key of the local npm repository. | `string` | `"npm-local"` | no |
| npm\_virtual\_repository\_key | Key of the virtual npm repository over the local npm repository. | `string` | `"npm"` | no |
| properties | Properties to set on item\_path, each a key with its values. | `map(list(string))` | ```{ "team": [ "platform" ], "tier": [ "gold", "silver" ] }``` | no |
| repository\_key | Key of the local generic repository. | `string` | `"generic-local"` | no |
| repository\_properties | Properties to set on the generic repository itself, each a key with its values. | `map(list(string))` | ```{ "owner": [ "platform" ], "stage": [ "test", "release" ] }``` | no |
| url | Base URL of artifactweave, without /artifactory, for example https://artifacts.example.com. | `string` | n/a | yes |
| with\_properties | Whether to set the properties on item\_path. Apply with false first, deploy the file, then apply with true. | `bool` | `true` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| item\_path | Path of the file the properties are set on, or null while with\_properties is false. |
| local\_generic\_repositories | Package type of each local generic repository, by key, as the repositories data source lists them. |
| package\_repository\_keys | Keys of the local npm, Go, Maven and Gradle repositories, by format. |
| repository\_key | Key of the local generic repository. |
| repository\_properties | Properties set on the generic repository itself. |
| virtual\_repository\_keys | Keys of the virtual npm repository and of the virtual Maven repository, which has no members. |
<!-- END_TF_DOCS -->
