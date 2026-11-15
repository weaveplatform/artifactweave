# virtual-repositories: every provider virtual repository resource

Thirty-three virtual repositories, one for each `artifactory_virtual_<type>_repository`
resource in the pinned JFrog Terraform provider, each over a local and a remote member and
deploying to the local one. Every virtual field the provider offers is set away from its
default, so the reference shows how to write each one and the empty plan after apply shows
artifactweave keeps it as written. It manages configuration only: applying it does not show
that artifactweave resolves or deploys through a virtual of that type, which the native
client scenarios cover.

## What it configures

- Virtual repositories of the eight families and generic: Docker, OCI, Helm OCI, npm, Go,
  Maven, Gradle, Ivy, SBT, PyPI, NuGet, Helm, Terraform and generic.
- Virtual repositories of every other type the provider has a virtual resource for: Alpine,
  Bower, Conan, Debian, RPM, CocoaPods, Composer, RubyGems, Git LFS, P2, Pub, Puppet,
  Swift, Ansible, Chef, Conda, CRAN, Hex and Nix.
- For each, a local member (`artifactory_local_<type>_repository`; Docker's is
  `docker_v2`, Terraform's a module registry, and P2's a Maven repository, since there is
  no local P2 repository and a P2 virtual aggregates only the Maven group) and a remote
  member (`artifactory_remote_<type>_repository`) whose URL is `<upstream_url>/<type>`.
- On every virtual: the description and notes, include and exclude patterns, a layout
  other than the provider's default for the type, remote retrieval for other Artifactory
  instances, and 404 for unauthorised resources.
- Type fields: the metadata retrieval cache period (Alpine, Ansible, Chef, Conan, Conda,
  CRAN, Debian, Helm, npm, RPM); external dependencies with their patterns and remote
  (npm, Bower; Go has no remote field and is set off); tag resolution by timestamp
  (Docker, OCI, Helm OCI); Helm namespaces; forced NuGet, Maven and Conan authentication;
  the POM reference cleanup policy and signing key pair (Maven, Gradle, Ivy, SBT); Debian's
  default architectures and extra index compression formats.
- Three key pairs (`artifactory_keypair`): two GPG pairs, which the Debian and RPM virtuals
  name as primary and secondary and the Maven family as `key_pair`, and an RSA pair, which
  the Alpine virtual and the three Hex repositories name.

The patterns, layouts and flags are chosen to differ from the defaults, not as advice:
choose your own for a virtual that serves clients.

## Prerequisites

- A running artifactweave and its base URL, without `/artifactory`.
- An admin access token.
- Two passphrase-less GPG key pairs and an RSA key pair, ASCII-armoured; the provider takes
  the RSA private key only in PKCS#1 form (`BEGIN RSA PRIVATE KEY`).
- An upstream base URL for the remote members. Creating a remote fetches nothing from it.
- `jfrog/artifactory` 12.11.14, pinned in `versions.tf` and the lock file.

## Inputs to set

```hcl
url               = "https://artifacts.example.com"
repository_prefix = "team-virtual"
upstream_url      = "https://upstream.example.com"
```

Supply `TF_VAR_access_token` and the key pairs (`TF_VAR_gpg_primary_private_key`,
`TF_VAR_gpg_primary_public_key`, `TF_VAR_gpg_secondary_private_key`,
`TF_VAR_gpg_secondary_public_key`, `TF_VAR_rsa_private_key` and `TF_VAR_rsa_public_key`)
through the environment. The key pair names default to `managed-virtual-gpg-primary`,
`managed-virtual-gpg-secondary` and `managed-virtual-rsa`.

## Applying it

```sh
terraform init
terraform apply
terraform plan -detailed-exitcode
```

The plan exits zero: every field reads back as written, and each virtual lists its members
in the order given. Set `revision = 2` to reverse every virtual's members and change one
field of each (the retrieval cache period, the external dependency patterns, the POM cleanup
policy, or the notes), then apply and plan again.

The provider posts a usage report to `/artifactory/api/system/usage` for each resource it
creates, reads, updates or deletes, and artifactweave counts each against the admin's
expensive request budget, 600 a minute by default (`api.expensive_principal_per_minute`).
Applying, refreshing and destroying this reference's 102 resources within a minute exceeds
it, and a refused write fails the run with 429. Raise the budget on the server, as the test
run does, or space the runs out.

## Security

The access token and the private keys are sensitive variables, so Terraform keeps them out
of plans and logs. They are still stored in the state, so keep the state private. The
configuration grants no permissions: who can read or deploy through a virtual is decided by
the permissions you manage elsewhere, for example with the [identity](../identity/README.md)
reference. `hide_unauthorized_resources` asks a virtual to answer 404 rather than 401 or 403
to a caller who may not read what it asked for; this reference stores the setting, and the
virtual resolution tickets of P10 own the answer.

The remotes carry no credentials. The reference provisions no cloud resources and needs no
external service credentials.

## Teardown

`terraform destroy` deletes the 99 repositories and the three key pairs.

## How this scenario is tested

The gated Terraform job runs this scenario through `test/terraform/run.sh` against one
coverage-instrumented server. It makes two GPG pairs with `tools/gpgkey` and an RSA pair
with `openssl`, points the remotes at the fixture server's `/controlled-upstream/<type>`,
applies revision 1, requires an empty refreshed plan, and reads every virtual through
`GET /artifactory/api/repositories/{key}` to check its members' order and deployment
repository. It then applies revision 2 and repeats both checks, plans once more and
destroys. No request goes to an upstream. The server runs with
`api.expensive_principal_per_minute` at 6000 for the reason above.

See [CI documentation](../../../../docs/testing/ci.md#terraform-scenarios) for the run,
and [provider research](../../../../docs/research/08-terraform-providers.md) for the
resource list.

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
| [artifactory_keypair.gpg_primary](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/keypair) | resource |
| [artifactory_keypair.gpg_secondary](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/keypair) | resource |
| [artifactory_keypair.rsa](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/keypair) | resource |
| [artifactory_local_alpine_repository.alpine](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_alpine_repository) | resource |
| [artifactory_local_ansible_repository.ansible](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_ansible_repository) | resource |
| [artifactory_local_bower_repository.bower](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_bower_repository) | resource |
| [artifactory_local_chef_repository.chef](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_chef_repository) | resource |
| [artifactory_local_cocoapods_repository.cocoapods](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_cocoapods_repository) | resource |
| [artifactory_local_composer_repository.composer](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_composer_repository) | resource |
| [artifactory_local_conan_repository.conan](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_conan_repository) | resource |
| [artifactory_local_conda_repository.conda](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_conda_repository) | resource |
| [artifactory_local_cran_repository.cran](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_cran_repository) | resource |
| [artifactory_local_debian_repository.debian](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_debian_repository) | resource |
| [artifactory_local_docker_v2_repository.docker](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_docker_v2_repository) | resource |
| [artifactory_local_gems_repository.gems](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_gems_repository) | resource |
| [artifactory_local_generic_repository.generic](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_generic_repository) | resource |
| [artifactory_local_gitlfs_repository.gitlfs](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_gitlfs_repository) | resource |
| [artifactory_local_go_repository.go](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_go_repository) | resource |
| [artifactory_local_gradle_repository.gradle](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_gradle_repository) | resource |
| [artifactory_local_helm_repository.helm](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_helm_repository) | resource |
| [artifactory_local_helmoci_repository.helmoci](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_helmoci_repository) | resource |
| [artifactory_local_hex_repository.hex](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_hex_repository) | resource |
| [artifactory_local_ivy_repository.ivy](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_ivy_repository) | resource |
| [artifactory_local_maven_repository.maven](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_maven_repository) | resource |
| [artifactory_local_maven_repository.p2](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_maven_repository) | resource |
| [artifactory_local_nix_repository.nix](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_nix_repository) | resource |
| [artifactory_local_npm_repository.npm](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_npm_repository) | resource |
| [artifactory_local_nuget_repository.nuget](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_nuget_repository) | resource |
| [artifactory_local_oci_repository.oci](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_oci_repository) | resource |
| [artifactory_local_pub_repository.pub](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_pub_repository) | resource |
| [artifactory_local_puppet_repository.puppet](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_puppet_repository) | resource |
| [artifactory_local_pypi_repository.pypi](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_pypi_repository) | resource |
| [artifactory_local_rpm_repository.rpm](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_rpm_repository) | resource |
| [artifactory_local_sbt_repository.sbt](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_sbt_repository) | resource |
| [artifactory_local_swift_repository.swift](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_swift_repository) | resource |
| [artifactory_local_terraform_module_repository.terraform](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/local_terraform_module_repository) | resource |
| [artifactory_remote_alpine_repository.alpine](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_alpine_repository) | resource |
| [artifactory_remote_ansible_repository.ansible](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_ansible_repository) | resource |
| [artifactory_remote_bower_repository.bower](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_bower_repository) | resource |
| [artifactory_remote_chef_repository.chef](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_chef_repository) | resource |
| [artifactory_remote_cocoapods_repository.cocoapods](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_cocoapods_repository) | resource |
| [artifactory_remote_composer_repository.composer](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_composer_repository) | resource |
| [artifactory_remote_conan_repository.conan](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_conan_repository) | resource |
| [artifactory_remote_conda_repository.conda](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_conda_repository) | resource |
| [artifactory_remote_cran_repository.cran](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_cran_repository) | resource |
| [artifactory_remote_debian_repository.debian](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_debian_repository) | resource |
| [artifactory_remote_docker_repository.docker](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_docker_repository) | resource |
| [artifactory_remote_gems_repository.gems](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_gems_repository) | resource |
| [artifactory_remote_generic_repository.generic](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_generic_repository) | resource |
| [artifactory_remote_gitlfs_repository.gitlfs](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_gitlfs_repository) | resource |
| [artifactory_remote_go_repository.go](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_go_repository) | resource |
| [artifactory_remote_gradle_repository.gradle](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_gradle_repository) | resource |
| [artifactory_remote_helm_repository.helm](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_helm_repository) | resource |
| [artifactory_remote_helmoci_repository.helmoci](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_helmoci_repository) | resource |
| [artifactory_remote_hex_repository.hex](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_hex_repository) | resource |
| [artifactory_remote_ivy_repository.ivy](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_ivy_repository) | resource |
| [artifactory_remote_maven_repository.maven](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_maven_repository) | resource |
| [artifactory_remote_nix_repository.nix](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_nix_repository) | resource |
| [artifactory_remote_npm_repository.npm](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_npm_repository) | resource |
| [artifactory_remote_nuget_repository.nuget](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_nuget_repository) | resource |
| [artifactory_remote_oci_repository.oci](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_oci_repository) | resource |
| [artifactory_remote_p2_repository.p2](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_p2_repository) | resource |
| [artifactory_remote_pub_repository.pub](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_pub_repository) | resource |
| [artifactory_remote_puppet_repository.puppet](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_puppet_repository) | resource |
| [artifactory_remote_pypi_repository.pypi](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_pypi_repository) | resource |
| [artifactory_remote_rpm_repository.rpm](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_rpm_repository) | resource |
| [artifactory_remote_sbt_repository.sbt](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_sbt_repository) | resource |
| [artifactory_remote_swift_repository.swift](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_swift_repository) | resource |
| [artifactory_remote_terraform_repository.terraform](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_terraform_repository) | resource |
| [artifactory_virtual_alpine_repository.alpine](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_alpine_repository) | resource |
| [artifactory_virtual_ansible_repository.ansible](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_ansible_repository) | resource |
| [artifactory_virtual_bower_repository.bower](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_bower_repository) | resource |
| [artifactory_virtual_chef_repository.chef](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_chef_repository) | resource |
| [artifactory_virtual_cocoapods_repository.cocoapods](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_cocoapods_repository) | resource |
| [artifactory_virtual_composer_repository.composer](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_composer_repository) | resource |
| [artifactory_virtual_conan_repository.conan](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_conan_repository) | resource |
| [artifactory_virtual_conda_repository.conda](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_conda_repository) | resource |
| [artifactory_virtual_cran_repository.cran](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_cran_repository) | resource |
| [artifactory_virtual_debian_repository.debian](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_debian_repository) | resource |
| [artifactory_virtual_docker_repository.docker](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_docker_repository) | resource |
| [artifactory_virtual_gems_repository.gems](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_gems_repository) | resource |
| [artifactory_virtual_generic_repository.generic](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_generic_repository) | resource |
| [artifactory_virtual_gitlfs_repository.gitlfs](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_gitlfs_repository) | resource |
| [artifactory_virtual_go_repository.go](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_go_repository) | resource |
| [artifactory_virtual_gradle_repository.gradle](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_gradle_repository) | resource |
| [artifactory_virtual_helm_repository.helm](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_helm_repository) | resource |
| [artifactory_virtual_helmoci_repository.helmoci](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_helmoci_repository) | resource |
| [artifactory_virtual_hex_repository.hex](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_hex_repository) | resource |
| [artifactory_virtual_ivy_repository.ivy](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_ivy_repository) | resource |
| [artifactory_virtual_maven_repository.maven](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_maven_repository) | resource |
| [artifactory_virtual_nix_repository.nix](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_nix_repository) | resource |
| [artifactory_virtual_npm_repository.npm](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_npm_repository) | resource |
| [artifactory_virtual_nuget_repository.nuget](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_nuget_repository) | resource |
| [artifactory_virtual_oci_repository.oci](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_oci_repository) | resource |
| [artifactory_virtual_p2_repository.p2](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_p2_repository) | resource |
| [artifactory_virtual_pub_repository.pub](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_pub_repository) | resource |
| [artifactory_virtual_puppet_repository.puppet](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_puppet_repository) | resource |
| [artifactory_virtual_pypi_repository.pypi](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_pypi_repository) | resource |
| [artifactory_virtual_rpm_repository.rpm](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_rpm_repository) | resource |
| [artifactory_virtual_sbt_repository.sbt](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_sbt_repository) | resource |
| [artifactory_virtual_swift_repository.swift](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_swift_repository) | resource |
| [artifactory_virtual_terraform_repository.terraform](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/virtual_terraform_repository) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| access\_token | Admin access token, sent as a Bearer token. | `string` | n/a | yes |
| gpg\_primary\_keypair\_name | Name of the GPG key pair the Debian and RPM virtuals sign with first, and the Maven-family virtuals' key\_pair. | `string` | `"managed-virtual-gpg-primary"` | no |
| gpg\_primary\_private\_key | ASCII-armoured private half of the primary GPG key pair, without a passphrase. | `string` | n/a | yes |
| gpg\_primary\_public\_key | ASCII-armoured public half of the primary GPG key pair. | `string` | n/a | yes |
| gpg\_secondary\_keypair\_name | Name of the GPG key pair the Debian and RPM virtuals name as their secondary pair. | `string` | `"managed-virtual-gpg-secondary"` | no |
| gpg\_secondary\_private\_key | ASCII-armoured private half of the secondary GPG key pair, without a passphrase. | `string` | n/a | yes |
| gpg\_secondary\_public\_key | ASCII-armoured public half of the secondary GPG key pair. | `string` | n/a | yes |
| repository\_prefix | Prefix of every repository key. A virtual is named prefix-type, and its members prefix-type-local and prefix-type-remote. | `string` | `"managed-virtual"` | no |
| revision | Configuration revision, 1 or 2. Revision 2 reverses every virtual's member order and changes one field of each. | `number` | `1` | no |
| rsa\_keypair\_name | Name of the RSA key pair the Alpine virtual and the Hex repositories sign with. | `string` | `"managed-virtual-rsa"` | no |
| rsa\_private\_key | PEM private half of the RSA key pair, in PKCS#1 form (BEGIN RSA PRIVATE KEY), which the provider requires. | `string` | n/a | yes |
| rsa\_public\_key | PEM public half of the RSA key pair. The Hex remote also takes it as its upstream's public key. | `string` | n/a | yes |
| upstream\_url | Base URL the remote members proxy. The remote of each type takes this URL with /type appended. Creating a remote fetches nothing from it. | `string` | n/a | yes |
| url | Base URL of artifactweave, without /artifactory. | `string` | n/a | yes |

### Outputs

| Name | Description |
| ---- | ----------- |
| virtual\_members | Each virtual's member keys by package type, in the order this revision gives them; the default deployment repository is the local one. |
| virtual\_repository\_keys | Virtual repository keys by package type. |
<!-- END_TF_DOCS -->
