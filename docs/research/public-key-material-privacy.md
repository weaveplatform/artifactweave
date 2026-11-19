# Public-key material validation and export (#1727)

A `publicKey` value is readable by ordinary authenticated callers and, when anonymous access is enabled, anonymous callers. Its complete contents must therefore be public. Omitting the separate `privateKey` and `passphrase` JSON fields cannot establish that invariant when the public field itself contains secret packets or trailing private blocks.

## Evidence and existing contract

- JFrog's [repository public-key operation](https://docs.jfrog.com/administration/reference/getkeypairpublickeyperrepository) describes a public-key attachment, with authenticated or anonymous-on access. The existing individual-pair projection and that access policy were settled in [PR #463](https://github.com/weaveplatform/artifactweave/pull/463). This repair retains them.
- Pinned [ProtonMail go-crypto 1.5.2](https://github.com/ProtonMail/go-crypto/blob/v1.5.2/openpgp/keys.go) deliberately reads public **or** private armored key rings. Its general-purpose parser is not a public-field validator. `armor.Decode` may skip leading bytes, read beyond the block and stop at the optional CRC; the packet reader may skip unsupported packets. These documented library behaviors must not leave parts of a publicly exported field unchecked.
- Main `621523d38c1cda8b822fbb1b52a7f4b36e5f7b5d` admitted three generated GPG variants: private armor, private packets relabeled as public armor, and a public block followed by private armor. The sole baseline used actual SQLite/PostgreSQL 17.12 handlers, twice per backend. Reader projections and Debian/RPM/Opkg repository public downloads returned the supplied private-bearing bytes. Logs retained only statuses and equality booleans.
- Separate generated-fixture unit negatives verify the related RSA/ECDSA/Ed25519 trailing-PEM paths and armored ECDSA path. Those are the same complete-field validation failure; they are not attributed to an unrelated vendor or library vulnerability.

## Boundaries

`keymaterial.CheckPublic` checks complete public fields without rewriting valid bytes or including material in errors. It examines every public PEM block or OpenPGP packet, including later entities and subkeys, and rejects secret/non-public packets and unexamined framing/remainders. It retains the library's existing optional-CRC verification behavior. Existing private-key parsing, algorithm/curve matching and passphrase validation run first in `CheckPair`, preserving their error ordering.

Admission uses this check, as do public pair summaries, lists and repository attachments. Stored rows are checked at export too: older versions could already have persisted contaminated values. Invalid stored public material returns the existing sanitized internal-error boundary, not raw bytes. Intended authentication and public-read access remain unchanged.

Terraform provider descriptors also exported stored primary/secondary GPG armor verbatim. A bounded existing-handler probe confirmed all four primary/secondary variants returned 200 with private packet data. Full-field rejection now retains that provider route's existing sanitized 409 for invalid public keys, aborting the whole descriptor rather than dropping a contaminated secondary. Parsed/re-serialized public consumers retain their existing behavior.

RPM checks public material before generating new local/federated or virtual key artifacts, and after decoding a warm virtual R8 envelope. The final file-delivery guard covers historical generated `repodata/repomd.xml.key` artifacts selected from local/federated RPM repositories, including a request through a Generic virtual. It validates one selected-SHA stream before headers, conditional answers or redirects, rewinds it and serves that same stream. A concurrent item replacement cannot substitute a different unexamined payload. Reads are bounded by the management API's existing 64 MiB body limit. Other paths, package types and remote-cache files keep their ordinary streaming behavior.

Existing contaminated records or generated artifacts fail closed until an administrator replaces the public field or regenerates the affected metadata through existing maintenance operations. The repair does not rewrite key records, delete artifacts, alter cache expiry or add a migration.

## Introduced regression retained in this repair

A generated-fixture negative caught an incomplete first implementation of PEM framing: `pem.Decode` could skip an unterminated outer public frame and nested OpenPGP secret armor before finding a later public PEM block. The repair rejects nested BEGIN framing before decoding; permanent unit and real SQLite/PostgreSQL admission controls retain that case, alongside a valid PEM-header control. This was corrected within #1727 before publication.

## Compatibility limits

The corrected 400 admission and 500 corrupt-stored-output answers use the project's existing invalid-material and internal-error boundaries. No live Artifactory capture establishes the precise vendor answer for these malformed/contaminated inputs.

## Verification

Synthetic fixtures cover valid public bytes, algorithms and passphrases; complete-field refusals; current and historical exports; admin/reader/anonymous access; local/federated RPM payloads; selected-item replacement; GET/HEAD/range/conditional responses; storage faults; and warm virtual-cache envelopes. Real SQLite/PostgreSQL handler tests use isolated scratch schemas. The PR records final current-source race, coverage and quality results separately from repository-wide CI.
