# 0019: Human authentication tiers

Status: Proposed

Checked against: Artifactory 7.161.32

## Context

Artifactory authenticates people through LDAP, SAML, Crowd, HTTP SSO and OIDC, provisions
them through SCIM, and the Terraform providers manage all of those settings
(`artifactory_ldap_setting_v2`, `platform_saml_settings`, `platform_scim_*`,
`platform_crowd_settings`, `platform_http_sso_settings`, `platform_oidc_configuration`).
The current implementation serves developers using package clients and administrators
using the CLI and Terraform. Browser login and provisioning also serve those users;
neither requires the full administrative web console planned for P39.

## Decision

Implementation sequence, governed by the
[capability roadmap](../../implementation/capability-roadmap.md):

- **Implemented through P8:** local users and groups, access tokens, OIDC token exchange for
  CI workload identity (`/access/api/v1/oidc` configurations, identity mappings and the
  token exchange endpoint), because CI pipelines use it to obtain tokens without stored
  secrets.
- **P12:** LDAP bind and group synchronisation through the Access LDAP endpoints.
- **P21:** browser and native-client approval flows, SAML, human OIDC/OAuth, Crowd,
  HTTP-SSO, MFA and session policy. Workload OIDC exchange does not implement human login.
- **P22:** SCIM discovery, user/group provisioning, updates and deprovisioning, including
  permission and token consequences. SCIM is an identity lifecycle API, independent of
  the full web console.

LDAP, SAML, Crowd, HTTP-SSO and SCIM currently answer `501`. That response describes
unfinished work, not an exclusion; their Terraform resource names remain inventoried in
[10-scope-and-tiers](../10-scope-and-tiers.md).

## Rationale

Local identities and workload exchange establish the token and permission model first.
Federated human login then gives developers authenticated browser approval and token
issuance, while SCIM keeps accounts and group membership aligned with an external
directory. These capabilities have consumers without a full product UI; the earlier
UI-based exclusion is replaced by [decision 0042](0042-capability-scope-and-supersession.md).

**Alternatives considered:**
- *Implementing every provider in the initial identity phase*: increases the first
  phase's delivery scope; the remaining providers have explicit phase owners instead.

## Constraints

- LDAP and federated login must keep a local administrator fallback.
- P21 must define trusted proxy headers, redirect validation, session expiry and MFA
  behavior; P22 must define ownership conflicts and deprovisioning effects before either
  phase claims compatibility.

## Verification

- OIDC exchange acceptance with a fake issuer.
- Current stub tests record the unimplemented endpoints; they are not completion evidence.
- P12: LDAP acceptance against a test directory container.
- P21: real identity-provider login and native-client approval, including denial, replay,
  expired sessions and untrusted proxy headers.
- P22: real SCIM provider create/update/group-sync/deactivate flows and resulting access
  and token revocation checks.

## References

- [05-platform-identity-and-security-apis](../05-platform-identity-and-security-apis.md)
- [10-scope-and-tiers](../10-scope-and-tiers.md)
- <https://docs.jfrog.com/administration/reference/jfrog-platform-rest-apis/llms.txt>
