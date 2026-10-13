# 0019: Human authentication tiers

Status: Proposed

## Context

Artifactory authenticates people through LDAP, SAML, Crowd, HTTP SSO and OIDC, provisions
them through SCIM, and the Terraform providers manage all of those settings
(`artifactory_ldap_setting_v2`, `platform_saml_settings`, `platform_scim_*`,
`platform_crowd_settings`, `platform_http_sso_settings`, `platform_oidc_configuration`).
artifactweave v1 has no user interface, so the humans it serves are developers using
package clients and administrators using the CLI and Terraform.

## Decision

Tiers:

- **Implemented in v1:** local users and groups, access tokens, OIDC token exchange for
  CI workload identity (`/access/api/v1/oidc` configurations, identity mappings and the
  token exchange endpoint), because CI pipelines use it to obtain tokens without stored
  secrets.
- **Implemented later:** LDAP bind and group synchronisation through the Access LDAP
  endpoints, because it is how most self-hosted teams map developers to groups.
- **Stubbed (`501`):** SAML, Crowd, HTTP SSO and SCIM, with their Terraform resource names
  listed in [10-scope-and-tiers](../10-scope-and-tiers.md) so that modules fail at a
  predictable resource.

## Rationale

Without a UI there is no browser login to federate; what CI and developers need is tokens
and group membership, which OIDC exchange and LDAP provide. SAML and SCIM only pay off
with a UI and are deferred with it.

**Alternatives considered:**
- *Implementing every provider in v1*: large surface with no consumer.

## Constraints

- The LDAP phase must keep the local-user model as the fallback and seed the admin
  locally.

## Verification

- OIDC exchange acceptance with a fake issuer.
- Stub tests for the SAML, Crowd, HTTP SSO and SCIM endpoints.
- LDAP acceptance against a test directory container when that phase lands.

## References

- [05-platform-identity-and-security-apis](../05-platform-identity-and-security-apis.md)
- [10-scope-and-tiers](../10-scope-and-tiers.md)
- <https://docs.jfrog.com/administration/reference/jfrog-platform-rest-apis/llms.txt>
