# remote-repositories: provider-managed remote repository configuration

Fourteen remote repository types managed through the pinned JFrog Terraform provider.
This reference demonstrates configuration, defaults, updates and secret handling. Native
package consumption is proven separately by the P9 adapter scenarios; applying a resource
does not establish that its upstream protocol is implemented.

## What it configures

- Generic, Docker, OCI, Helm OCI, npm and Go remote repositories.
- Maven, Gradle, Ivy and SBT remote repositories, including checksum, eager-fetch and
  release/snapshot settings.
- PyPI, NuGet and classic Helm remote repositories with their format-specific endpoints.
- One Terraform remote repository with distinct module registry and provider URLs. The
  pinned provider has `artifactory_remote_terraform_repository`; it has no remote
  `terraformbackend` resource. A remote state backend is an invalid repository-class
  combination, rejected by the API. Local Terraform state backends remain supported.
- A reference to an existing named proxy on the generic remote, which also exercises authentication, headers,
  cache/fetch policy and content-synchronisation settings. These management fields retain
  their individual runtime owners, including property synchronisation in P12/#303.

The proxy definition is supplied through the implemented YAML descriptor. The separate
`artifactory_proxy` resource needs XML descriptor reads, which remain P12/#303; this
reference does not claim provider CRUD for that resource.

Project-scoped remote repositories retain P11/#487 ownership.

## Prerequisites

- A running artifactweave and its base URL, without `/artifactory`.
- An admin access token, an existing proxy definition and appropriate upstream URLs and credentials.
- Terraform 1.11 or later for ephemeral variables and write-only provider attributes;
  `jfrog/artifactory` 12.11.14 is pinned in `versions.tf` and the lock file.

## Inputs to set

```hcl
url               = "https://artifacts.example.com"
repository_prefix = "team-remote"
proxy_key         = "team-proxy"
upstream_urls = {
  generic   = "https://upstream.example.com/files"
  docker    = "https://registry.example.com"
  oci       = "https://registry.example.com"
  helmoci   = "https://registry.example.com"
  npm       = "https://upstream.example.com/npm"
  go        = "https://upstream.example.com/go"
  maven     = "https://upstream.example.com/maven"
  gradle    = "https://upstream.example.com/gradle"
  ivy       = "https://upstream.example.com/ivy"
  sbt       = "https://upstream.example.com/sbt"
  pypi      = "https://upstream.example.com/pypi"
  nuget     = "https://upstream.example.com/nuget"
  helm      = "https://upstream.example.com/helm"
  terraform = "https://upstream.example.com/terraform"
}
```

Supply `TF_VAR_access_token`, `TF_VAR_upstream_password` and `TF_VAR_header_secret`
through the environment. The PyPI, NuGet, Helm and Terraform
resource blocks derive their additional URLs from these bases; adjust them to your
upstream's discovery contract before using the reference for package consumption.

## Applying it

```sh
terraform init
terraform apply
terraform plan -detailed-exitcode
```

The plan exits zero after refresh. Set `revision = 2` and change the supplied credentials
to exercise updates and write-only password rotation, then apply and plan again. Revision 2
also takes the generic remote offline and disables persistence, so review those settings
before using this example on a live repository.

## Security

All secret inputs are sensitive. The upstream password is also ephemeral and reaches the
provider through `password_wo`, so Terraform does not store it in state or a saved plan.
`password_wo_version` signals rotation. Custom header values are sensitive but remain in Terraform state; keep state private.
Artifactweave seals remote passwords, sensitive headers and descriptor proxy credentials and redacts repository API reads. A public custom header remains visible.

The fixture uses test-only credentials and loopback URLs. It provisions no cloud resources
and requires no external service credentials. Configuration of a proxy or endpoint alone
makes no outbound request to that service.

## Teardown

`terraform destroy` deletes the fourteen repositories. The existing proxy remains. Any cached content
in these repositories is removed with them.

## How this scenario is tested

The gated Terraform job runs this scenario through `test/terraform/run.sh` against one
coverage-instrumented server. It applies revision 1, requires an empty refreshed plan,
checks API fields/defaults and secret redaction, rotates credentials and applies revision 2,
checks configuration and persistent secret sealing, requires another empty plan, and
destroys the repositories and removes its descriptor-managed proxy. It also proves the API rejects remote `terraformbackend` configuration.
All upstream URLs point at the fixture server's controlled paths; no native content is
requested. The provider fixture's coverage joins the existing merged quality gate.

See [CI documentation](../../../../docs/testing/ci.md#terraform-scenarios) for the run
and timing evidence, and [provider research](../../../../docs/research/08-terraform-providers.md)
for the resource/API distinction.

## Reference

Generated by terraform-docs (`make deploy-docs`).

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.11 |
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
| [artifactory_remote_docker_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_docker_repository) | resource |
| [artifactory_remote_generic_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_generic_repository) | resource |
| [artifactory_remote_go_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_go_repository) | resource |
| [artifactory_remote_gradle_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_gradle_repository) | resource |
| [artifactory_remote_helm_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_helm_repository) | resource |
| [artifactory_remote_helmoci_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_helmoci_repository) | resource |
| [artifactory_remote_ivy_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_ivy_repository) | resource |
| [artifactory_remote_maven_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_maven_repository) | resource |
| [artifactory_remote_npm_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_npm_repository) | resource |
| [artifactory_remote_nuget_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_nuget_repository) | resource |
| [artifactory_remote_oci_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_oci_repository) | resource |
| [artifactory_remote_pypi_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_pypi_repository) | resource |
| [artifactory_remote_sbt_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_sbt_repository) | resource |
| [artifactory_remote_terraform_repository.this](https://registry.terraform.io/providers/jfrog/artifactory/12.11.14/docs/resources/remote_terraform_repository) | resource |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| access\_token | Admin access token, sent as a Bearer token. | `string` | n/a | yes |
| header\_secret | Sensitive generic upstream header value. Terraform stores it in state; artifactweave seals it and redacts API reads. | `string` | n/a | yes |
| proxy\_key | Existing proxy definition referenced by the generic remote. Manage it through the YAML descriptor until P12 implements XML descriptor reads used by artifactory\_proxy. | `string` | n/a | yes |
| repository\_prefix | Prefix for all fourteen remote repository keys. | `string` | `"managed-remote"` | no |
| revision | Configuration revision, 1 or 2. Revision 2 demonstrates updates, disables generic persistence and takes that repository offline. | `number` | `1` | no |
| upstream\_password | Generic upstream password passed to the provider's write-only attribute; never stored in Terraform state. Rotate it by increasing revision. | `string` | n/a | yes |
| upstream\_urls | Upstream base URL for each of the fourteen package formats in this reference. Terraform registry and provider endpoints are derived separately from its base URL. | `map(string)` | n/a | yes |
| url | Base URL of artifactweave, without /artifactory. | `string` | n/a | yes |

### Outputs

| Name | Description |
| ---- | ----------- |
| repository\_keys | Remote repository keys by package format. |
<!-- END_TF_DOCS -->
