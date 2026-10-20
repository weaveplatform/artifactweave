# Backup and restore

Status: skeleton. Completed in P18.

artifactweave has two state stores:

| Store | Contents | Backup |
|---|---|---|
| Database (SQLite file or PostgreSQL) | Repositories, items, properties, statistics, users, groups, permissions, tokens, projects, webhooks, policies, run reports | SQLite: copy the file while the process is stopped, or use the SQLite online backup API through `artifactweave backup` (P18). PostgreSQL: `pg_dump` |
| Blob store (local directory or S3 bucket) | Content-addressed blobs | Filesystem snapshot or bucket versioning. Blobs are immutable, so incremental copies are safe |

The master key (decision [0017](../research/decisions/0017-sealed-columns-and-master-key.md))
is not in either store. Without it, sealed columns (remote passwords, webhook secrets,
private keys) cannot be read after a restore, and neither can the Terraform states that
`terraformbackend` repositories store while `encryptStates` is on, which are sealed in the
blob store (decision [0041](../research/decisions/0041-terraform-backend.md)). Keep it in the
secret manager that provides `ARTIFACTWEAVE_MASTER_KEY`. `artifactweave keys rotate` re-seals
both the sealed columns and those states under the new key, so `master_key_previous` can go
once it has run. A sealed state copied to another instance opens only under the same master
key, which is why a backend repository replicated between instances needs `encryptStates`
off, as 7.x requires.

Restore order: blob store, then database, then start the process. A database restored
without its blobs reports the missing content in `GET /artifactory/api/storageinfo` and the
affected items return 404 until the blobs are back.

The Artifactory import and export APIs are out of scope for v1 (research
[10](../research/10-scope-and-tiers.md)).
