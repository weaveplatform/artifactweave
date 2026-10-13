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
| `blob.backend` | | | `fs` | `fs` or `s3` |
| `blob.fs.root` | | | `./data/blobs` | Local blob directory |
| `blob.s3.*` | | `AWS_*` | | Bucket, prefix, endpoint, region, path style |
| `master_key` | | `ARTIFACTWEAVE_MASTER_KEY` | required | Key for sealed columns (decision 0017): 32 bytes or more, base64 or raw, optionally prefixed with its version as `vN:` (no prefix means version 1) |
| `master_key_previous` | | `ARTIFACTWEAVE_MASTER_KEY_PREVIOUS` | unset | The key being rotated away from, kept until `artifactweave keys rotate` has re-sealed every value (see below) |
| `bootstrap.admin_password` | | `ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD` | | Sets the seeded `admin` password on first start |
| `bootstrap.descriptor` | `--bootstrap` | | | Descriptor YAML applied on first start |
| `docker.subdomain_match` | | | off | Enables the subdomain access method in addition to repository-path routing |
| `log.level`, `log.format` | | | `info`, `json` | slog settings |
| `distribution.import_dir` | | `ARTIFACTWEAVE_DISTRIBUTION_IMPORT_DIR` | `<data_dir>/import` | Release bundle import directory: `POST /distribution/api/release/import` and `/artifactory/api/release/import` read archives by file name from here and never outside it (decision 0023) |
| `distribution.edge_role` | | | `false` | Makes the instance an edge: ordinary deploys are refused and content arrives only through distribution (decision 0023) |
| `cold_blob.backend`, `cold_blob.fs.root`, `cold_blob.s3.*` | | `AWS_*` | unset | Cold blob tier for archive policies, same backends and keys as `blob` (an S3 storage class can be set); without it archive runs fail with a clear error (decision 0024) |
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

