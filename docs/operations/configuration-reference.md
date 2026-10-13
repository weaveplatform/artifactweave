# Configuration reference

Status: skeleton. Filled in as each phase lands; the strict app YAML schema is the source
of truth and `artifactweave config validate` rejects unknown keys.

artifactweave has three configuration layers (decision
[0009](../research/decisions/0009-configuration-as-code.md)):

1. **Application configuration** (`artifactweave.yaml`): how the process runs. Precedence
   is flag, then environment variable, then file.
2. **Platform configuration as code**: the Artifactory YAML descriptor format, applied
   with `artifactweave config apply <file>`, with `--bootstrap <file>` at start, or with
   `PATCH /artifactory/api/system/configuration`. This is where repositories, proxies,
   property sets, layouts, backups and security settings live.
3. **The REST APIs**, including the JFrog Terraform providers.

## Application configuration keys (P0 draft)

| Key | Flag | Env | Default | Meaning |
|---|---|---|---|---|
| `listen` | `--listen` | `ARTIFACTWEAVE_LISTEN` | `:8081` | HTTP listen address |
| `base_url` | `--base-url` | `ARTIFACTWEAVE_BASE_URL` | derived from the request | Public base URL used in embedded links when no override header is present |
| `compat.artifactory_version` | | `ARTIFACTWEAVE_COMPAT_ARTIFACTORY_VERSION` | `7.155.0` | Version string reported to clients (decision 0005) |
| `database.driver` | | | `sqlite` | `sqlite` or `postgres` |
| `database.dsn` | | `ARTIFACTWEAVE_DATABASE_DSN` | `<data_dir>/artifactweave.db` | SQLite file path (an optional `file:` prefix is ignored) or PostgreSQL connection URL |
| `blob.backend` | | `ARTIFACTWEAVE_BLOB_BACKEND` | `fs` | `fs`, `s3`, `gcs` or `azure` (decision [0027](../research/decisions/0027-cloud-blob-backends.md)) |
| `blob.fs.root` | | `ARTIFACTWEAVE_BLOB_FS_ROOT` | `./data/blobs` | Local blob directory |
| `blob.s3.bucket`, `prefix`, `endpoint`, `region`, `path_style` | | `ARTIFACTWEAVE_BLOB_S3_*`; credentials `AWS_*` | | S3-compatible store; `bucket` required |
| `blob.gcs.bucket`, `prefix`, `endpoint` | | `ARTIFACTWEAVE_BLOB_GCS_*` | | Google Cloud Storage; `bucket` required; `endpoint` only for emulators |
| `blob.gcs.credentials_file` | | `ARTIFACTWEAVE_BLOB_GCS_CREDENTIALS_FILE` | Application Default Credentials | Service-account key file; empty uses ADC, which covers GKE Workload Identity |
| `blob.gcs.google_access_id` | | | from the credentials | Service account that signs download URLs when the credentials do not name one |
| `blob.gcs.part_size` | | | `16MiB` | Upload part size; a multiple of `256KiB`, at least `1MiB` |
| `blob.azure.account`, `endpoint`, `container`, `prefix` | | `ARTIFACTWEAVE_BLOB_AZURE_*` | endpoint `https://<account>.blob.core.windows.net/` | Azure Blob Storage; `container` and an account or endpoint required |
| `blob.azure.credential` | | `ARTIFACTWEAVE_BLOB_AZURE_CREDENTIAL` | from the secrets set | `shared_key`, `sas`, `connection_string`, `default` (the Azure SDK chain), `managed_identity` or `workload_identity`; empty picks the kind of the secret that is set, else `default` |
| `blob.azure.account_key`, `sas_token`, `connection_string` | | `ARTIFACTWEAVE_BLOB_AZURE_ACCOUNT_KEY` and so on | | Secrets: set them in the environment rather than the file; redacted from every summary |
| `blob.azure.managed_identity_client_id` | | | | User-assigned managed identity |
| `blob.azure.part_size`, `copy_range_size`, `concurrency` | | | `8MiB`, `100MiB`, `8` | Block size (at most `4000MiB`), the range of each server-side copy call, and how many copy calls run at once |
| `master_key` | | `ARTIFACTWEAVE_MASTER_KEY` | required | Key for sealed columns (decision 0017): 32 bytes or more, base64 or raw, optionally prefixed with its version as `vN:` (no prefix means version 1) |
| `master_key_previous` | | `ARTIFACTWEAVE_MASTER_KEY_PREVIOUS` | unset | The key being rotated away from, kept until `artifactweave keys rotate` has re-sealed every value (see below) |
| `bootstrap.admin_password` | | `ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD` | | Sets the seeded `admin` password on first start |
| `bootstrap.descriptor` | `--bootstrap` | | | Descriptor YAML applied on first start |
| `docker.subdomain_match` | | | off | Enables the subdomain access method in addition to repository-path routing |
| `log.level`, `log.format` | | | `info`, `json` | slog settings |
| `distribution.import_dir` | | `ARTIFACTWEAVE_DISTRIBUTION_IMPORT_DIR` | `<data_dir>/import` | Release bundle import directory: `POST /distribution/api/release/import` and `/artifactory/api/release/import` read archives by file name from here and never outside it (decision 0023) |
| `distribution.edge_role` | | | `false` | Makes the instance an edge: ordinary deploys are refused and content arrives only through distribution (decision 0023) |
| `blob.s3.profile` | | `ARTIFACTWEAVE_BLOB_S3_PROFILE` | default profile | Named profile of the shared AWS configuration, so stores can use different accounts |
| `blob.stores.<name>.*` | | `ARTIFACTWEAVE_BLOB_STORES_<NAME>_*` for stores the file declares | | A named store: `backend` (`fs`, `s3`, `gcs` or `azure`) and that backend's keys as above, `zone` (a region or site label) and `write_enabled` (default `true`; `false` drains the store: no new copies, reads continue). Names are 1 to 32 of `a-z`, `0-9` and `_`. An `fs` store without `fs.root` uses `<data_dir>/blob-stores/<name>`, so no two stores share a directory |
| `blob.backend: mirror` | | | | Keep every blob in several named stores (decision [0028](../research/decisions/0028-blob-mirroring.md)) |
| `blob.mirror.members` | | `ARTIFACTWEAVE_BLOB_MIRROR_MEMBERS` (comma-separated) | | The stores that hold copies. Each mirrored upload holds one chunk in memory: the smallest multiple of every member's `part_size` that is at least 8 MiB, which is 16 MiB with the default part sizes. Part sizes that need more than 256 MiB stop the mirror from starting |
| `blob.mirror.redundancy` | | `ARTIFACTWEAVE_BLOB_MIRROR_REDUNDANCY` | every member | Copies of each blob; fewer than the members places copies zone first, then round robin |
| `blob.mirror.write_quorum` | | `ARTIFACTWEAVE_BLOB_MIRROR_WRITE_QUORUM` | redundancy minus 1, at least 1 | Copies stored before an upload succeeds (Artifactory's `lenientLimit`); missing copies are repaired in the background. The server refuses to start unless 1 ≤ quorum ≤ redundancy ≤ members and at least quorum members are write-enabled |
| `blob.mirror.zone` | | `ARTIFACTWEAVE_BLOB_MIRROR_ZONE` | | This instance's zone: reads and writes prefer stores in it |
| `blob.mirror.member_timeout` | | `ARTIFACTWEAVE_BLOB_MIRROR_MEMBER_TIMEOUT` | `30s` | How long a store may finish after the quorum before its copy is left to repair |
| `cold_blob.*` | | `ARTIFACTWEAVE_COLD_BLOB_*` | unset | Cold blob tier for archive policies: the same backends and keys as `blob`; an empty `cold_blob.backend` means none is configured, and archive runs then fail with a clear error (decision 0024). An `fs` cold tier without `fs.root` uses `<data_dir>/cold-blobs`, and its named `fs` stores `<data_dir>/cold-blob-stores/<name>` |
| `peer.outbox_retention` | | | `168h` | How long outbox entries are kept for a peer that has not acknowledged them; a peer behind the window needs a full sync (decision 0021) |
| `workers.timeout` | | | `20s` | Overall wall-clock limit per worker execution (decision 0026) |
| `workers.call_timeout` | | | `5s` | Wall-clock limit per call into the worker |
| `workers.memory_limit` | | | `64MiB` | Heap a single execution may allocate; the helper is interrupted above it and killed at twice it |
| `workers.cpu_limit` | | | `1s` | CPU time per execution |
| `workers.max_call_depth` | | | `100` | JavaScript call stack depth |
| `workers.payload_limit` | | | `100KiB` | Size of the event payload in and the result out |
| `workers.source_limit` | | | `100000` | Characters of TypeScript source accepted at save time |
| `workers.hosts` | | | `min(4, GOMAXPROCS)` | Helper processes running executions; each runs one at a time |
| `workers.queue_timeout` | | | `30s` | How long an execution may wait for a free helper before failing |
| `workers.host_recycle_after` | | | `1000` | Executions after which a helper is replaced |
| `workers.http_allow_list` | | | empty | Hosts a worker's outbound HTTP client may reach; loopback and link-local are always refused (decision 0026) |

A mirror across two AWS regions and an Azure account, where the instance runs in
`eu-west-1`; the Azure store is draining:

```yaml
blob:
  backend: mirror
  stores:
    aws_use1: {backend: s3, zone: us-east-1, s3: {bucket: aw-use1, region: us-east-1}}
    aws_euw1: {backend: s3, zone: eu-west-1, s3: {bucket: aw-euw1, region: eu-west-1}}
    azure_weu:
      backend: azure
      zone: westeurope
      write_enabled: false
      azure: {account: awstore, container: blobs, credential: managed_identity}
  mirror:
    members: [aws_use1, aws_euw1, azure_weu]
    zone: eu-west-1
```

Sizes (`part_size`, `copy_range_size`) are a number of bytes or a number with a binary
suffix: `512KiB`, `16MiB`, `1GiB`.

Secrets inside descriptor files may be written as `${ENV_NAME}` references.

## Schema migrations

The server applies pending migrations when it opens the store. The `migrate` commands
manage the schema by hand and need no master key:

| Command | Effect |
|---|---|
| `artifactweave migrate version` | Prints the applied and the latest schema version |
| `artifactweave migrate up` | Applies every pending migration, each in its own transaction |
| `artifactweave migrate down [--to N]` | Reverts the latest migration, or every migration newer than `N` (`--to 0` reverts everything) |

## Rotating the master key

Sealed values record the key version that sealed them, so versions must stay stable
across restarts:

1. Move the current key to `master_key_previous` unchanged, and set `master_key` to the
   new key with the next version prefix, for example `v2:<new key>` (then `v3:` at the
   following rotation).
2. Run `artifactweave keys rotate`, which re-seals every sealed value under the new
   version.
3. Remove `master_key_previous`.

The server opens values sealed under either key while both are configured, so it can run
throughout.

