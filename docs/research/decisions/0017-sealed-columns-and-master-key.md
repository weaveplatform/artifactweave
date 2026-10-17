# 0017: Sealed columns and master key

Status: Proposed

Checked against: Artifactory 7.161.32

## Context

The configuration holds secrets: remote repository passwords, proxy passwords, LDAP
manager passwords, mail server credentials, webhook secrets, key pair private keys and
passphrases, OIDC secrets and the token signing key. Since the enterprise products came
into scope it also holds the instance's peer private key
([0021](0021-peer-trust-and-sync-protocol.md)), the OpenPGP signing keys and passphrases
for release bundles ([0023](0023-release-bundles-and-distribution.md)), and worker secrets
([0026](0026-workers-runtime.md)). Artifactory returns masked or
encrypted values on read, and the Terraform providers compare what they read. The default
store is a SQLite file on disk. hostweave's decision 0014 defined a keyring with purpose
and row additional authenticated data.

## Decision

`internal/crypt` ports hostweave's keyring: a master key from the application
configuration (file, environment variable or a KMS reference later), AES-GCM sealing with
a purpose string and the row identity as additional authenticated data, and key versions
so `artifactweave keys rotate` re-wraps every sealed column. Every secret listed above is
a sealed column; plaintext never reaches the store.

On read, each endpoint applies the masking rule the research records for it: repository
configuration returns the password field masked, the XML descriptor returns an encrypted
token the way Artifactory does, webhook secrets are redacted, key pair reads omit the
private key, the peer private key and the bundle signing keys are never returned by any
endpoint, and worker secrets are listed by name only. Pairing tokens are not stored: only
the hash of an unused token's nonce is kept until it is redeemed or expires.

## Rationale

A single-binary registry will often run with SQLite on a disk that is backed up and
copied; secrets in plaintext there are a release blocker. Reusing the hostweave design
keeps one audited implementation across the platform.

**Alternatives considered:**
- *Relying on disk encryption*: does not survive backups, exports or copies.
- *Environment-variable references only*: fine for GitOps input, but the API accepts
  literal secrets too.

## Constraints

- Losing the master key makes every sealed value unreadable; the operator guide covers
  key backup.
- Rotation keeps the previous key until every row is re-wrapped.

## Verification

- Seal and unseal round trips, wrong-key and tampered-AAD failures.
- Rotation test over a populated store.
- Masking tests per endpoint against the research table.

## References

- [06-system-configuration-and-descriptor](../06-system-configuration-and-descriptor.md)
- `weaveplatform/hostweave@main`, `docs/research/decisions/0014-secrets-provider-and-keyring.md`
