# Managing artifactweave with Terraform

artifactweave serves the APIs the JFrog Terraform providers call, so the same HCL that
manages Artifactory manages artifactweave. Each directory under `terraform/` is a reference
configuration for the application layer: what lives inside a running artifactweave. The
infrastructure it runs on has its own references in `deploy/platform`
([decision 0030](../../docs/research/decisions/0030-reference-deployments.md)). The
`terraform` CI job applies every reference here against a running artifactweave, requires an
empty plan and destroys it, so a reference that stops working fails the build (see
[docs/testing/ci.md](../../docs/testing/ci.md#terraform-scenarios)).

| Scenario | What it configures | Inputs without a default |
|---|---|---|
| [repositories](terraform/repositories/README.md) | A local generic repository (`artifactory_local_generic_repository`) and properties on the repository itself and on a file deployed into it (`artifactory_item_properties`) | `url`, `access_token` |
| [identity](terraform/identity/README.md) | Users (`artifactory_user`, `artifactory_managed_user`), groups (`platform_group`, `artifactory_group`), a permission over one repository (`platform_permission`, read back with the `artifactory_permission_target` data source), a repository-scoped token (`artifactory_scoped_token`) and a signing key pair (`artifactory_keypair`) | `url`, `access_token`, `user_password`, `keypair_private_key`, `keypair_public_key` |
| [cdn-repositories](terraform/cdn-repositories/README.md) | Repositories whose downloads redirect: one with `cdnRedirect` and one with `downloadRedirect` (`artifactory_local_generic_repository`), for a store with a CDN such as the `cdn` reference deployments render | `url`, `access_token` |
| [oidc](terraform/oidc/README.md) | OIDC token exchange for CI: a GitHub Actions provider and a generic one (`platform_oidc_configuration`), each with an identity mapping (`platform_oidc_identity_mapping`), one for a named user and one for a group through a synthetic subject | `url`, `access_token`, `github_organization`, `ci_project_path` |

Every name a reference creates (repository keys, users, groups, permissions, OIDC providers)
is an input with a default you can keep or change. Each README follows
[TEMPLATE.md](TEMPLATE.md): what it configures, prerequisites, inputs, applying, security,
teardown and how it is tested ([decision 0030](../../docs/research/decisions/0030-reference-deployments.md),
rules A1 to A4).

## Providers

| Provider | Manages | Pinned version |
|---|---|---|
| `jfrog/artifactory` | Repositories, items, users, groups, scoped tokens, key pairs | 12.11.14 |
| `jfrog/platform` | Access groups and permissions, OIDC configurations and identity mappings | 2.2.11 |
| `jfrog/project` | Projects with their members, roles and environments | not yet used by a reference |

All three take the base URL without `/artifactory` and an access token, which they send as a
Bearer token:

```hcl
provider "artifactory" {
  url          = "https://artifactweave.example.com"
  access_token = var.access_token
}

provider "platform" {
  url          = "https://artifactweave.example.com"
  access_token = var.access_token
}
```

`artifactory_permission_target` is a stub in provider 12.x: every operation fails with
"deprecated. Use platform_permission instead". Use `platform_permission`; artifactweave
serves the same permission through the Access v2, v2 and v1 permission APIs.

## Authentication

The providers do not accept a password. Give them an access token, either:
- **minted by an administrator**, with `POST /access/api/v1/tokens` or `jf atc`, scoped to
  `applied-permissions/admin` for configuration that manages users and permissions; or
- **exchanged in CI** through OIDC token exchange
  ([decision 0005](../../docs/research/decisions/0005-authentication-and-tokens.md),
  [research 21](../../docs/research/21-oidc-token-exchange.md)). A pipeline presents its
  identity provider's ID token to `POST /access/api/v1/oidc/token`, for example with
  `jf eot` or `jfrog/setup-jfrog-cli` with `oidc-provider-name`, and receives a short-lived
  token with no stored secret. The [oidc](terraform/oidc) reference sets up the provider and
  identity mapping that the exchange needs.

Pass the token in a sensitive variable, for example `TF_VAR_access_token`; never commit it.

## Version policy

Each reference pins its providers exactly (`version = "12.11.14"`, not a range), and
`test/terraform/run.sh` pins the same versions with the SHA-256 of every release zip it
installs. The `version` attributes, the checksums in `run.sh` and each scenario's
`.terraform.lock.hcl` move together, by hand, following the steps in
[docs/testing/ci.md](../../docs/testing/ci.md#terraform-scenarios). Dependabot leaves these
roots alone, because it would change a version without the checksums `run.sh` checks it
against.

## Applying a reference to your own instance

The references run outside the CI harness, against any artifactweave or Artifactory:

```sh
cd deploy/artifactweave/terraform/oidc
export TF_VAR_url=https://artifactweave.example.com
export TF_VAR_access_token=...   # an admin token
terraform init                   # installs the pinned providers, checked against the lock file
terraform plan
```

Each scenario's README lists its inputs. The repositories reference needs two applies,
because the provider cannot deploy a file (see its [README](terraform/repositories/README.md)),
and the identity reference takes a user password and a PKCS#1 key pair as inputs.
