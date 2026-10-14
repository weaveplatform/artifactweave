# Managing artifactweave with Terraform

artifactweave serves the APIs the JFrog Terraform providers call, so the same HCL that
manages Artifactory manages artifactweave. Each directory under `terraform/` is a reference
configuration, and the `terraform` CI job applies it against a running artifactweave,
requires an empty plan and destroys it (see
[docs/testing/ci.md](../../docs/testing/ci.md#terraform-scenarios)).

| Scenario | What it manages |
|---|---|
| [identity](terraform/identity) | Users (`artifactory_user`, `artifactory_managed_user`), groups (`platform_group`, `artifactory_group`), a permission over one repository (`platform_permission`, read back with the `artifactory_permission_target` data source), a repository-scoped token (`artifactory_scoped_token`) and a signing key pair (`artifactory_keypair`) |
| [oidc](terraform/oidc) | OIDC token exchange for CI: a GitHub Actions provider and a generic one (`platform_oidc_configuration`), each with an identity mapping (`platform_oidc_identity_mapping`), one for a named user and one for a group through a synthetic subject |

## Providers and authentication

The scenarios pin `jfrog/artifactory` 12.11.14 and `jfrog/platform` 2.2.11. Both take the
base URL without `/artifactory` and an access token, which they send as a Bearer token:

```hcl
provider "artifactory" {
  url          = "https://artifactweave.example.com"
  access_token = var.access_token
}
```

Mint an admin token with `POST /access/api/v1/tokens`, or with `jf atc`. The providers do
not accept a password.

`artifactory_permission_target` is a stub in provider 12.x: every operation fails with
"deprecated. Use platform_permission instead". Use `platform_permission`; artifactweave
serves the same permission through the Access v2, v2 and v1 permission APIs.
