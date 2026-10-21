# Backup and restore

Status: operational baseline for P0–P8. P18 owns the initial recovery tooling and drills;
P36 owns complete migration, scheduled backup and import/export workflows. There is no
`artifactweave backup` command yet. The
[capability roadmap](../implementation/capability-roadmap.md) tracks both phases.

artifactweave has two state stores:

| Store | Contents | Backup |
|---|---|---|
| Database (SQLite file or PostgreSQL) | Repository and item metadata, package indexes, properties, statistics, identities, permissions, tokens, build-info, settings, work queue and blob references | SQLite: copy the database and any WAL sidecar after stopping every process using it. PostgreSQL: `pg_dump` |
| Blob store (filesystem, S3, GCS, Azure or a mirror of named stores) | Content-addressed artifacts, derived files and Terraform state bytes | Snapshot or copy the configured directories/buckets. Preserve every referenced blob; for a mirror, preserve the member configuration and the copies the database records |

Stop writers and background work while taking a coordinated database/blob backup, and
keep the application configuration with it. Immutability permits incremental blob copies,
but garbage collection can remove unreferenced blobs during a live copy; an independently
taken database dump and bucket snapshot are not necessarily a consistent recovery point.

The master key (decision [0017](../research/decisions/0017-sealed-columns-and-master-key.md))
is not in either store. Without it, sealed columns (remote passwords, webhook secrets,
private keys) cannot be read after a restore, and neither can the Terraform states that
`terraformbackend` repositories store while `encryptStates` is on, which are sealed in the
blob store (decision [0041](../research/decisions/0041-terraform-backend.md)). Keep it in the
secret manager that provides `ARTIFACTWEAVE_MASTER_KEY`. `artifactweave keys rotate` re-seals
both the sealed columns and those states under the new key, so `master_key_previous` can go
once it has run. A sealed state copied to another instance opens only under the same master
key. Cross-instance repository replication remains planned in P11, with advanced and
external interoperability in P37; the intended backend
replication contract requires `encryptStates` off, as 7.x does. Retain the key versions
needed by older backups even after retiring them from the running instance.

Restore the blob stores, database, configuration and keys before starting the process.
Verify representative artifact downloads and encrypted Terraform state reads after the
restore. `GET /artifactory/api/storageinfo` reports metadata totals and, for mirrors,
recorded replica health; it does not scan every blob to validate a backup. Missing content
can therefore leave metadata present while downloads fail.

The Artifactory import/export and Access backup APIs currently answer `501`. P36 owns
their implementation, bootstrap-format migration, key handling and resumable transfer,
including a restore that proves identities, permissions, metadata and content survive
together. These APIs are in scope; the manual procedure above does not provide their
compatibility contract. P35 must establish coordinated recovery for HA deployments, and
P41 qualifies disaster recovery across the expanded platform.
