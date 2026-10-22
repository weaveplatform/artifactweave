# Sensitive repository HTTP header storage

[#652](https://github.com/weaveplatform/artifactweave/issues/652) closes a persistence
gap discovered by the P9 Terraform management qualification: API masking already hid
sensitive remote HTTP header values, but the SQL repository document still held them
in plaintext. Both SQLite and PostgreSQL now store these values in
`repository_header_secrets`, separate from `repositories.doc`.

With a configured keyring, each value uses the existing AES-GCM sealed-column format.
Its authenticated identity includes the repository key, header position and exact
header name. Copying ciphertext between repositories or headers therefore fails.
Renaming or reordering a header through a normal update reseals its value under its
new identity. Duplicate names remain distinct stored positions; outbound validation
still rejects ambiguous request-header configuration. An intentionally empty sensitive
header has an explicit NULL row, while a missing row fails closed.

The repository document retains header names, sensitivity flags, ordering and
non-sensitive values. Create/update never mutates the caller's configuration while
redacting the stored document. Get and List reconstruct the internal configuration
from a single SQL statement, so concurrent updates cannot pair a stale document with
new credentials. HTTP responses continue to use existing masking. A sensitive header
returned with an empty value in an update retains its stored value through the existing
configuration merge. Removing a header deletes its secret row; repository deletion
cascades to all its header rows. Repository keys remain immutable in the API.

Source identity and credential revisions use the restored values. Rotating a header
therefore changes the source generation and resolver revision, fencing prior remote
requests. A storage-only reseal changes ciphertext without changing effective header
values. `artifactweave keys rotate` includes this table and uses the same conditional
updates as other sealed columns, so it cannot replace a concurrent header update with
an older value. Keep previous key versions until rotation completes.

## Upgrade behavior

Schema migration 0016 creates the secret table and marks existing repository rows for
a key-aware backfill. Normal store startup performs that backfill before serving
requests. It reads bounded batches and locks each repository while saving sealed
headers, redacting its document and completing its marker in one transaction.
Unrelated document fields, repository timestamps and source generations are preserved.
A failed conversion leaves that repository unchanged and aborts startup; retrying with
the problem corrected resumes remaining rows. Completed rows are not converted again.
An existing sealed repository password is checked before converting legacy nonempty
headers, preventing conversion under a key that cannot open that repository's existing
credentials.

`migrate up` performs schema changes without requiring a master key; normal startup
then completes the data backfill with the configured keyring. `keys rotate` also
completes any pending backfill. Stop older binaries before the upgrade: they do not
know the new secret table and could write sensitive values into documents again while
leaving the completed backfill marker intact. Mixed-version writers are unsupported:
new readers fail closed on that inconsistent state, and historical plaintext remains.
Downgrading schema 0016 discards the new secret table; use a compatible backup for an
application rollback, rather than treating the down migration as a secret-preserving
conversion to the former plaintext format.

Explicitly unencrypted stores retain the existing no-key behavior: values are separate
from the document but are plaintext in the secret table. Configure the keyring and run
`keys rotate` to seal those values, as with repository passwords. Backfill does not
securely erase historical SQLite pages or WAL files, PostgreSQL WAL, snapshots, logs or
backups that already contain plaintext. Handle those copies under the installation's
existing secret-retention policy and rotate exposed upstream credentials when required.

The shared SQLite/PostgreSQL contracts cover round-trip reads and listings, masked
updates, environment renames, renamed/reordered headers, source revision changes, ciphertext isolation,
removal, schema-15 upgrade and old-key retirement. Fault tests cover transaction
rollback, failed backfill/retry, missing or moved secret rows and incorrect keys.
The pinned-provider management fixture in #637 adds API and persistence qualification.
This implementation updates [ADR 0017](../research/decisions/0017-sealed-columns-and-master-key.md)
without changing its decision status.
