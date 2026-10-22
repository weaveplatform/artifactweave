# 0017: Sealed columns and master key

Status: Proposed

Checked against: Artifactory 7.161.32

## Context

The configuration holds secrets: remote repository passwords, sensitive custom HTTP
header values, proxy passwords, LDAP
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
configuration (file or environment variable), AES-GCM sealing with
a purpose string and the row identity as additional authenticated data, and key versions
so `artifactweave keys rotate` re-wraps every sealed column. Every secret listed above is
a sealed column when a keyring is configured. Explicitly unencrypted stores retain
plaintext secret columns until a keyring is configured and `keys rotate` runs.

External master-key sources, KMS and Vault integration belong to P38 and are not accepted
configuration today. The implemented keyring and rotation also cover encrypted Terraform
backend state blobs ([0041](0041-terraform-backend.md)); each future feature must use that
sealing boundary when it adds secrets. P23 owns signing/key controls, P36 owns key-aware
migration and recovery, and P38 owns operational key and certificate integrations, as
assigned in the [capability roadmap](../../implementation/capability-roadmap.md).

On read, each endpoint applies the masking rule the research records for it: repository
configuration returns the password field masked, the XML descriptor returns an encrypted
token the way Artifactory does, webhook secrets are redacted, key pair reads omit the
private key, the peer private key and the bundle signing keys are never returned by any
endpoint, and worker secrets are listed by name only. Pairing tokens are not stored: only
the hash of an unused token's nonce is kept until it is redeemed or expires.

## Sensitive repository header checkpoint (#652)

[Sensitive header storage](../../implementation/repository-header-sealing.md) now keeps
values outside repository JSON, sealed against the repository/header identity. SQL
migration 0016 and startup backfill convert existing documents atomically per repository;
get/list joins reconstruct one consistent configuration snapshot. Rename/reorder,
masked update, removal, key rotation and source revision behavior are covered on both
SQL stores. The upgrade cleans live documents; it does not erase historical database
pages, WAL or backups. This checkpoint does not change the decision status.

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
