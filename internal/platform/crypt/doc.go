// Package crypt seals secret columns at rest (decision record 0017): remote
// repository and proxy passwords, LDAP and mail credentials, webhook secrets,
// key pair private keys, OIDC secrets and the token signing key.
//
// A sealed value is
//
//	magic(4) | version(4, big endian) | key id(4) | nonce(12) | AES-256-GCM ciphertext and tag
//
// Keys are derived from the operator's master key with HKDF-SHA256, one per
// version, so rotation adds a version instead of replacing the key: values
// sealed under an older version still open until the store re-seals them
// (Reseal), and the old master key can then be retired. The key id is a
// fingerprint of the master key for that version; it turns "the server was
// started with a different master key" into ErrWrongKey rather than an
// authentication failure that looks like tampering.
//
// The purpose (which column) and row id (which record) are authenticated but
// not stored, so a sealed value copied into another row or column fails to
// open. This is the same design as hostweave's keyring, with a version number
// in place of a key name because artifactweave has one master key at a time.
//
// # Role
//
// platform layer: sealing of secret columns at rest under the operator's
// master key. The SQL stores seal their secret columns with it and the
// Terraform state backend seals states with it; the composition root builds
// the one keyring they share. No module owns the capability, so it sits in
// the platform, and it imports only the standard library.
//
// # Inputs
//
//   - Master key material. ParseMasterKey reads a configured master key,
//     base64 or raw, of at least MinMasterKey bytes; ParseVersionedKey also
//     reads its optional "vN:" version prefix. New builds a keyring from one
//     key as version 1, and NewKeyring builds one from every configured
//     version: master_key and, during a rotation, master_key_previous.
//   - For each value, a purpose that names the column (for example
//     "keypairs.private_key") and a row id that names the record. For
//     Derive, a purpose that names what the derived key is for.
//
// # Outputs
//
//   - Keyring: Seal seals under the active version; Open opens a value
//     sealed under any version the ring holds; Reseal re-seals a value
//     sealed under an older version; Rotate returns a ring with the next
//     version added; Derive returns a 32-byte key per purpose from the
//     active version, for a secret that every node must hold without storing
//     it; Active and Versions report the versions.
//   - IsSealed and Version, which read the header of a stored value.
//   - Masked, Mask and KeepIfMasked: the masked form of a secret on read,
//     and what an update keeps when a client sends that form back.
//   - MinMasterKey, and the eight sentinels callers test with errors.Is:
//     ErrWeakKey, ErrNoKeys, ErrBadVersion, ErrBadFormat, ErrUnknownKey,
//     ErrWrongKey, ErrTampered and ErrBadPurpose.
//
// # Invariants
//
//   - The sealed layout above, magic, hkdfSalt ("artifactweave/crypt/v1")
//     and the HKDF labels in derive and Derive are persisted state: every
//     sealed column and every sealed Terraform state was written with them.
//     A change compiles, and the round-trip tests still pass because they
//     seal and open with the code of the same build; the known-answer tests
//     fail. A changed salt or label derives other keys, so stored values
//     stop opening (ErrWrongKey, or ErrTampered when only the sealing label
//     changed) or the keys Derive returns change. At run time a changed
//     magic or layout fails silently: the stores and the Terraform state
//     backend take a value without the header for one written unsealed and
//     return it as it is (IsSealed), so they would hand out ciphertext as
//     the secret, and keys rotate would seal the column values a second
//     time.
//   - A master key keeps its version for as long as values sealed under it
//     exist, because each sealed value names the version that sealed it.
//   - Purpose and row id are authenticated, not stored. The purpose is
//     length prefixed in the additional data, so ("ab", "c") and ("a", "bc")
//     authenticate differently.
//   - A Keyring never changes once built (Rotate returns a new one), so it
//     is safe for concurrent use.
//   - Error texts reach operators, for example when a weak master key stops
//     the server at startup, so they stay byte-identical.
//
// # References
//
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md
//     (sealed columns, the master key and masking on read).
//   - Decision 0040: docs/research/decisions/0040-terraform.md (the derived
//     key that signs Terraform's download URLs), and decision 0041:
//     docs/research/decisions/0041-terraform-backend.md (sealed Terraform
//     states).
//   - The artifactweave keys rotate command (cmd/artifactweave), which
//     re-seals every sealed column and Terraform state under the newest
//     version: docs/operations/configuration-reference.md, "Rotating the
//     master key"; docs/operations/backup-restore.md on keeping the key.
//   - hostweave's keyring, the same design: weaveplatform/hostweave,
//     docs/research/decisions/0014-secrets-provider-and-keyring.md.
//   - Research 03 and 15: docs/research/03-repository-configuration-model.md
//     and docs/research/15-data-model.md (Artifactory returns secrets
//     masked).
//   - HKDF: RFC 5869 (https://www.rfc-editor.org/rfc/rfc5869).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules": the platform imports no module.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 154 (platform/crypt) and line 993 (the mapping row).
//   - Plan: docs/implementation/plan.md#p2-stores, task 4, and
//     #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/978
//
// # Status
//
// Implemented P2: the versioned keyring, sealed columns, rotation with the
// keys rotate command, and the masking helpers. P8 added Derive, first for
// the signed download URLs of the Terraform registry; the Terraform state
// backend, remote repositories (P9) and Hugging Face (P9c) derive keys with
// it too. Moved here in P9d.
//
// # Testing
//
//   - TestUnitSealedFormatKnownAnswer and TestUnitDeriveKnownAnswer: with a
//     fixed master key and nonce, a value sealed under version 1, one sealed
//     under version 2 of a rotated keyring and a derived key equal golden
//     bytes, and each golden value opens only with its own purpose and row
//     id. They pin the persisted state under Invariants.
//   - TestUnitSealOpenRoundTrip: values round trip, a sealed value carries
//     the header and not the plaintext, and two seals of one value differ.
//   - TestUnitDerive: a derived key is the same for the same master key and
//     purpose, differs by purpose and by master key, and changes on rotation.
//   - TestUnitOpenRejects and TestUnitPurposeAndRowCannotBeShifted: each way
//     a value fails to open, with its sentinel.
//   - TestUnitRotation, TestUnitResealSealFailure and
//     TestUnitSealNonceFailure: a rotated ring opens old values and seals
//     under the new version, Reseal re-seals only older values and still
//     authenticates current ones, and a nonce failure is an error, never a
//     sealed value.
//   - TestUnitNewKeyringValidates, TestUnitParseMasterKey and
//     TestUnitParseVersionedKey: key material, versions and their refusals.
//   - TestUnitMasking: Mask and KeepIfMasked.
//
// Over real stores, the SQL stores' TestAccKeyRotation (SQLite and
// PostgreSQL), TestAccSealedColumnsComplete and TestAccRotateKeys (the keys
// rotate command) exercise the keyring.
package crypt
