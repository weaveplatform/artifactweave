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

SAML, Crowd, HTTP-SSO and SCIM currently answer `501`; LDAP
connections are managed since LDAP-01
([#1832](https://github.com/weaveplatform/artifactweave/issues/1832)), administrators only,
their manager passwords sealed and never returned, LDAP-02
([#1833](https://github.com/weaveplatform/artifactweave/issues/1833)) authenticates Basic
logins and tests connections against the directory
([research 05](../05-platform-identity-and-security-apis.md#ldap-login)), and LDAP-03
([#1834](https://github.com/weaveplatform/artifactweave/issues/1834)) synchronises LDAP
groups and makes them each fresh login's memberships
([research 05](../05-platform-identity-and-security-apis.md#ldap-groups)). That response describes
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

- LDAP and federated login must keep a local administrator fallback. LDAP-02 meets it by
  checking the internal password before any directory, so the local administrator logs
  in while every directory is unreachable.
- **Conflict with 7.x's internal password default (CFG-04,
  [#1828](https://github.com/weaveplatform/artifactweave/issues/1828)).** From 7.104,
  enabling LDAP turns "Disable Internal Password Login" on by default, and 7.x exempts no
  one but the UI-only list of "Internal users allowed to use basic authentication".
  artifactweave follows 7.x: the first enabled connection turns internal password login
  off while the switch was never written, and the local administrator's password stops
  working with everyone else's. The fallback this constraint asks for is therefore not
  the internal password by default but either of: an administrator access token, which
  the switch never affects; or the switch written on
  (`PUT /access/api/v1/config/security/authentication/basic_authentication_enabled`),
  before the connection is enabled or afterwards with a token, which no connection write
  overwrites. With the switch on, the ordering above still holds. An installation without
  LDAP sees no change
  ([research 05](../05-platform-identity-and-security-apis.md#internal-password-login)).
- No metadata transaction spans directory I/O: the directory answers first, then one
  guarded transaction rechecks the connection's revision and the user before it publishes
  the login, and a connection's change ends its logins, tokens and downloads at once.
- P21 must define trusted proxy headers, redirect validation, session expiry and MFA
  behavior; P22 must define ownership conflicts and deprovisioning effects before either
  phase claims compatibility.

## Verification

- OIDC exchange acceptance with a fake issuer.
- Current stub tests record the unimplemented endpoints; they are not completion evidence.
- P12: LDAP acceptance against a test directory container: an OpenLDAP container pinned
  by digest, over LDAP and LDAPS, on SQLite and PostgreSQL (LDAP-02), including internal
  and directory passwords with internal password login on and off and LDAP enabled and
  disabled, and the default the first connection writes (CFG-04).
- P21: real identity-provider login and native-client approval, including denial, replay,
  expired sessions and untrusted proxy headers.
- P22: real SCIM provider create/update/group-sync/deactivate flows and resulting access
  and token revocation checks.

## References

- [05-platform-identity-and-security-apis](../05-platform-identity-and-security-apis.md)
- [10-scope-and-tiers](../10-scope-and-tiers.md)
- <https://docs.jfrog.com/administration/reference/jfrog-platform-rest-apis/llms.txt>
