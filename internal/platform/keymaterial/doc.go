// Package keymaterial checks key material and opens it for signing: that the
// private and public halves of an RSA, GPG, Ed25519 or ECDSA key pair belong
// together, that a client certificate bundle carries its own private key,
// and how a stored RSA, Ed25519 or ECDSA key is read back to sign or verify
// format metadata.
//
// # Role
//
// platform layer: key-material checks that no module owns. Access's key pair
// and certificate API checks what it stores with them, and the Alpine, Hex,
// Nix and npm formats open their signing keys with them. A package inside access
// would make those formats import access internals, so it sits in the
// platform. It imports the standard library and the ProtonMail go-crypto
// OpenPGP package, and no module.
//
// # Inputs
//
//   - PEM private keys (PKCS#1, PKCS#8 or SEC 1), optionally encrypted the
//     legacy OpenSSL way, with the passphrase that unlocks them.
//   - PEM public keys (PKIX or PKCS#1) and certificates.
//   - ASCII-armored OpenPGP keys for GPG key pairs, and for ECDSA key pairs
//     exported from gpg, as 7.x's npm documentation has users make them.
//   - A key pair type, as the security API names it: RSA, GPG, Ed25519 or
//     ECDSA.
//
// # Outputs
//
//   - CheckPair: nil when the two halves belong to one key of the type.
//   - RSAPrivateKey, Ed25519PrivateKey, Ed25519PublicKey, ECDSAPrivateKey
//     and ECDSAPublicKey: the parsed keys a format signs or verifies its
//     metadata with. An ECDSA key is on P-256, P-384 or P-521.
//   - ParseBundle: a Bundle with the PEM a certificate upload stores and the
//     subject, issuer, validity and fingerprint of its leaf certificate.
//   - ErrInvalid, which every refusal wraps.
//
// # Invariants
//
//   - Error texts are observable: the key pair and certificate API returns
//     them in its 400 responses, so they stay byte-identical.
//   - The package keeps no state and writes no logs, so it never stores or
//     logs the key material it is given; the stores seal that material at
//     rest (decision 0017).
//   - A wrong passphrase on a legacy encrypted PEM key is reported as such
//     even when the decryption happens to pass the padding check, because
//     that encryption has no integrity check.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Package placement and names", "Names the migration introduces" and,
//     under Rationale, "keymaterial in the platform".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 1007, which maps the package to access/core/keymaterial; decision
//     0044 records the deviation.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md,
//     "Legacy Artifactory security API" (the key pair and certificate rows).
//   - https://docs.jfrog.com/administration/reference/createkeypair and
//     https://docs.jfrog.com/administration/reference/verifykeypair
//   - https://docs.jfrog.com/administration/docs/security-keys-management
//     (the four signing key types) and
//     https://docs.jfrog.com/artifactory/docs/npm-repositories, "Enable
//     ECDSA Signing in Local Repositories" (decision 0034, "Signatures").
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     ACC (https://github.com/weaveplatform/artifactweave/issues/940), and
//     #p4-identity-and-authorization.
//   - https://github.com/weaveplatform/artifactweave/issues/964
//
// # Status
//
// Implemented P4 (key pair and certificate checks) and P9c (the RSA and
// Ed25519 signing keys of the Alpine, Hex and Nix formats). Moved here in
// P9d. ECDSA pairs for npm package signatures (#362).
//
// # Testing
//
//   - TestUnitCheckRSA: RSA pairs from PKCS#1, PKCS#8, legacy encrypted and
//     certificate halves, and each refusal, from mismatched halves to an
//     unknown pair type.
//   - TestUnitCheckRSAWrongPassphraseWithValidPadding: a wrong passphrase
//     that passes the padding check is still reported as a wrong passphrase.
//   - TestUnitCheckGPG: armored OpenPGP pairs, with and without a
//     passphrase, and their refusals.
//   - TestUnitEd25519KeyMaterial: Ed25519 pairs, the two key parsers and
//     the pairs that are refused.
//   - TestUnitECDSAKeyMaterial: ECDSA pairs as PEM and as gpg's armored
//     export, with and without a passphrase, and the pairs that are refused,
//     other curves among them.
//   - TestUnitParseBundle: a certificate with its chain and key, the
//     subject, issuer, validity and fingerprint read from the leaf, and the
//     bundles that are refused.
//
// The tests generate their keys and never commit one, since a private key in
// the repository would also trip secret scanning.
package keymaterial
