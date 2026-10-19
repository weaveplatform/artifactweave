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
| `blob.gcs.cdn.cloud_cdn.domain`, `key_name`, `key_file` | | `ARTIFACTWEAVE_BLOB_GCS_CDN_CLOUD_CDN_*` | unset | A Cloud CDN backend bucket in front of the bucket (decision [0029](../research/decisions/0029-cdn-delivery-and-download-redirect.md)). A repository with `cdnRedirect` or `downloadRedirect` then answers downloads with a 302 to a signed URL on `domain`, unless the client sends `X-JFrog-Download-Redirect-To: S3` or `None`. `domain` is the load balancer's host, or an `http://` URL for a test edge; `key_name` is the key's name on the backend bucket (1 to 63 lowercase letters, digits or hyphens, starting with a letter); `key_file` holds the 128-bit key, base64url, as `gcloud compute backend-buckets add-signed-url-key` reads it, read and checked at start. All three are required together. The server refuses to start when `domain` is the `base_url` host or a subdomain of it: use a sibling host. Cloud CDN does not refuse unsigned requests itself, so the bucket must be private, readable only by the `cloud-cdn-fill` service account. Allowed under `blob` and `blob.stores.<name>`, refused under `cold_blob` |
| `blob.gcs.cdn.cloud_cdn.path_prefix` | | | empty | The path inside the bucket that the load balancer serves at its root, when that is not the bucket root. It must contain `blob.gcs.prefix`, or the store refuses to start |
| `blob.azure.account`, `endpoint`, `container`, `prefix` | | `ARTIFACTWEAVE_BLOB_AZURE_*` | endpoint `https://<account>.blob.core.windows.net/` | Azure Blob Storage; `container` and an account or endpoint required |
| `blob.azure.credential` | | `ARTIFACTWEAVE_BLOB_AZURE_CREDENTIAL` | from the secrets set | `shared_key`, `sas`, `connection_string`, `default` (the Azure SDK chain), `managed_identity` or `workload_identity`; empty picks the kind of the secret that is set, else `default` |
| `blob.azure.account_key`, `sas_token`, `connection_string` | | `ARTIFACTWEAVE_BLOB_AZURE_ACCOUNT_KEY` and so on | | Secrets: set them in the environment rather than the file; redacted from every summary |
| `blob.azure.managed_identity_client_id` | | | | User-assigned managed identity |
| `blob.azure.part_size`, `copy_range_size`, `concurrency` | | | `8MiB`, `4000MiB`, `8` | Block size (at most `4000MiB`), the range of each server-side copy call, and how many copy calls run at once |
| `master_key` | | `ARTIFACTWEAVE_MASTER_KEY` | required | Key for sealed columns (decision 0017): 32 bytes or more, base64 or raw, optionally prefixed with its version as `vN:` (no prefix means version 1) |
| `master_key_previous` | | `ARTIFACTWEAVE_MASTER_KEY_PREVIOUS` | unset | The key being rotated away from, kept until `artifactweave keys rotate` has re-sealed every value (see below) |
| `bootstrap.admin_password` | | `ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD` | generated | The seeded `admin` password, read on first start only. When unset, a random password is written to `<data_dir>/initial-admin-password` (mode 0600 on Unix) and the log names the file, never the password |
| `bootstrap.descriptor` | `--bootstrap` (on `serve`) | `ARTIFACTWEAVE_BOOTSTRAP_DESCRIPTOR` | | Descriptor YAML applied once, after the first start's principals; see [Configuration as code](#configuration-as-code) |
| `docker.subdomain_match` | | `ARTIFACTWEAVE_DOCKER_SUBDOMAIN_MATCH` | off | Adds the Docker subdomain access method to repository-path routing: a request whose host is `<repo>.<base_url host>`, where `<repo>` is a Docker, OCI or Helm OCI repository, addresses that repository ([reverse proxy](reverse-proxy.md#docker)). Needs `base_url`; the server refuses to start without it |
| `log.level`, `log.format` | | | `info`, `json` | slog settings |
| `gc.interval` | | `ARTIFACTWEAVE_GC_INTERVAL` | `4h` | Time between scheduled garbage collection passes, Artifactory's default schedule. `POST /api/system/storage/gc` runs a pass at any time (decision 0010) |
| `gc.grace_period` | | `ARTIFACTWEAVE_GC_GRACE_PERIOD` | `1h` | How long content stays unreferenced before garbage collection deletes it. It must outlast the slowest upload, because content is written before the reference that keeps it |
| `work.workers` | | `ARTIFACTWEAVE_WORK_WORKERS` | `4` | How many items of the persistent work queue run at once on this node (decision [0033](../research/decisions/0033-persistent-work-queue.md)). The queue runs derived work, such as package metadata, after the change that needs it |
| `work.lease` | | `ARTIFACTWEAVE_WORK_LEASE` | `2m` | How long a run holds its work item before another node may take it over; a running item's lease is renewed every half lease. At least `1s` |
| `work.max_attempts` | | `ARTIFACTWEAVE_WORK_MAX_ATTEMPTS` | `10` | How many runs a work item gets before it is parked. Retries wait from one second, doubling, up to ten minutes; a parked item runs again when its work is next asked for |
| `npm.max_publish_size` | | `ARTIFACTWEAVE_NPM_MAX_PUBLISH_SIZE` | `1GiB` | Largest `npm publish` request an npm repository accepts; a larger one answers 413 before the tarball is stored. The request carries the tarball in base64, so the largest tarball is about three quarters of this. A REST deploy of a tarball, as `jf npm publish` sends by default, is not limited by it (decision [0034](../research/decisions/0034-npm.md#publishing)) |
| `download.redirect.min_size` | | `ARTIFACTWEAVE_DOWNLOAD_REDIRECT_MIN_SIZE` | `1MiB` | Smallest file a repository with `downloadRedirect` or `cdnRedirect` answers with a 302 to a signed URL; smaller files stream. `0` redirects every file (decision [0029](../research/decisions/0029-cdn-delivery-and-download-redirect.md)). It is the default only: once a descriptor applies `downloadRedirectConfig.fileMinimumSize` (see [Configuration as code](#configuration-as-code)), that value, in MB of 1 MiB, takes precedence on every instance sharing the store, whatever this key, its variable or a restart says. A store with a CloudFront distribution sends no file over 50 GB to it: such a file goes to the object store under `downloadRedirect`, and streams under `cdnRedirect` alone |
| `download.redirect.ttl` | | `ARTIFACTWEAVE_DOWNLOAD_REDIRECT_TTL` | `5m` | How long a signed download URL stays valid once handed out; more than zero and at most `168h`, the S3 and GCS limit. CDN URLs, and storage URLs from a GCS store that signs through IAM `signBlob`, are issued on windows this long and reused within a window, so they stay valid for between this and twice this; a GCS store caches up to 4096 such URLs, and a TTL over `84h` is not windowed. Keep it below the session lifetime of temporary storage credentials, which end a URL's validity early. At start the log says which routes the blob store signs (`storage`, `cdn`) and warns about every repository whose flags ask for one it cannot sign; those downloads stream |
| `download.folder.max_concurrent_requests` | | `ARTIFACTWEAVE_DOWNLOAD_FOLDER_MAX_CONCURRENT_REQUESTS` | `10` | How many folder archive downloads (`GET /artifactory/api/archive/download/...`) run at once on this node; one more answers 400 `There are too many folder download requests currently running. Try again later.` until one finishes. At least `1`. Artifactory's "Max Parallel Folder Downloads" (`folderDownloadConfig.maxConcurrentRequests`). Whether folder downloads are on, and their size and file limits, are the folder download setting, not application configuration |
| `search.aql.max_results` | | `ARTIFACTWEAVE_SEARCH_AQL_MAX_RESULTS` | `500000` | Most rows `POST /api/search/aql` returns for one query, its own `.limit()` included; a trimmed result carries `range.notification`. It also caps every GET search's results. `0` removes the cap, as self-managed Artifactory has none (decision [0012](../research/decisions/0012-search-and-aql.md), [DV-030](../implementation/deviations.md#api-behaviour)) |
| `search.aql.non_admin_scan_factor` | | `ARTIFACTWEAVE_SEARCH_AQL_NON_ADMIN_SCAN_FACTOR` | `1` | For a non-admin query with `.limit(N)`, read N times this many rows before dropping those the caller may not read, so up to N readable rows come back. At least `1`; a `scan-factor` request header above 1 overrides it for that request, as Artifactory's `artifactory.aql.query.non.admin.scan.factor` does |
| `aql_timeout.query` | | `ARTIFACTWEAVE_AQL_TIMEOUT_QUERY` | `15m` | How long `POST /api/search/aql` may read the database for one query before it is cancelled and answered with 408. `0` sets no timeout. Artifactory's `artifactory.aql.query.timeout.seconds`, 900 by default (decision [0012](../research/decisions/0012-search-and-aql.md)) |
| `aql_timeout.streamed` | | `ARTIFACTWEAVE_AQL_TIMEOUT_STREAMED` | `2h` | The same for a query sent with a `stream: true` header, as Artifactory's `artifactory.aql.streamed.query.timeout`, 7,200 seconds by default |
| `search.user_query_limit` | | `ARTIFACTWEAVE_SEARCH_USER_QUERY_LIMIT` | `1000` | Most results a GET search (quick, property, checksum, usage, dates, creation and GAVC) returns to an anonymous caller, and to every non-admin when `search.limit_anonymous_users_only` is `false`; extra results are dropped silently. `0` removes the limit. Artifactory's `artifactory.search.userQueryLimit` (decision [0012](../research/decisions/0012-search-and-aql.md)) |
| `search.limit_anonymous_users_only` | | `ARTIFACTWEAVE_SEARCH_LIMIT_ANONYMOUS_USERS_ONLY` | `true` | Whether `search.user_query_limit` spares signed-in users; administrators are never limited. Artifactory's `artifactory.search.limitAnonymousUsersOnly` |
| `token.allow_basic_auth` | | `ARTIFACTWEAVE_TOKEN_ALLOW_BASIC_AUTH` | `false` | Lets a user name and password sent with Basic create tokens through `POST /access/api/v1/tokens`. Off, the request answers 401 `Unsupported authentication method Basic`, and a token must create the first token: an access or reference token, as Bearer or as the Basic password, always may. The legacy `POST /artifactory/api/security/token`, where `jf c add` with a password swaps it for a token, takes a password either way. Artifactory's `token.allow-basic-auth`, "Enable Token Generation via API" (decision [0005](../research/decisions/0005-authentication-and-tokens.md)) |
| `environments.global_limit` | | `ARTIFACTWEAVE_ENVIRONMENTS_GLOBAL_LIMIT` | `10` | Most global environments that may exist, the built-in `DEV` and `PROD` among them; `POST /access/api/v1/environments` answers 400 once that many exist. At least `1`. Access's `environments.global-limit` (decision [0013](../research/decisions/0013-projects-and-environments.md)) |
| `build.block_duplicate_entries` | | `ARTIFACTWEAVE_BUILD_BLOCK_DUPLICATE_ENTRIES` | `true` | Refuses, with 400, a build-info publish or append whose module lists one file twice: two artifacts with the same name, path and checksums, or two dependencies with the same id and checksums. `false` accepts them. Artifactory's `artifactory.build.block.duplicate.entries` (decision [0014](../research/decisions/0014-build-info.md)) |
| `deploy.enforce_permission_check_on_identical_checksum_deploy` | | `ARTIFACTWEAVE_DEPLOY_ENFORCE_PERMISSION_CHECK_ON_IDENTICAL_CHECKSUM_DEPLOY` | `false` | Makes a checksum deploy (`X-Checksum-Deploy: true`) over a file with the same content need Delete/Overwrite permission, answering 403 without it. Off, replacing a file with its own content needs Deploy alone: a checksum deploy, or an upload that declares the file's SHA-1 or SHA-256 and carries the same bytes, so a CI job that runs `jf rt u` again with a user who may not delete succeeds. Uploads follow that rule either way, and the JFrog CLI uploads the file when a checksum deploy is refused. Replacing a file with other content always needs Delete/Overwrite. Artifactory's `enforce.permission.check.on.identical.checksum.deploy` ([known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues), 7.26.3, RTDEV-24969; decision [0004](../research/decisions/0004-storage-sql-metadata-and-blobs.md#artifactory-7x-check)) |
| `distribution.import_dir` | | `ARTIFACTWEAVE_DISTRIBUTION_IMPORT_DIR` | `<data_dir>/import` | Release bundle import directory: `POST /distribution/api/release/import` and `/artifactory/api/release/import` read archives by file name from here and never outside it (decision 0023) |
| `distribution.edge_role` | | | `false` | Makes the instance an edge: ordinary deploys are refused and content arrives only through distribution (decision 0023) |
| `blob.s3.profile` | | `ARTIFACTWEAVE_BLOB_S3_PROFILE` | default profile | Named profile of the shared AWS configuration, so stores can use different accounts |
| `blob.s3.cdn.cloudfront.domain`, `key_pair_id`, `private_key_file` | | `ARTIFACTWEAVE_BLOB_S3_CDN_CLOUDFRONT_*` | unset | A CloudFront distribution in front of the bucket (decision [0029](../research/decisions/0029-cdn-delivery-and-download-redirect.md)). A repository with `cdnRedirect` or `downloadRedirect` then answers downloads with a 302 to a canned-policy signed URL on `domain`, unless the client sends `X-JFrog-Download-Redirect-To: S3` or `None`. `domain` is the distribution host (`d111.cloudfront.net` or a custom name), or an `http://` URL for a test edge; `key_pair_id` is the public key's ID in one of the distribution's trusted key groups; `private_key_file` is its PEM key, RSA (PKCS#1 or PKCS#8) or ECDSA (SEC 1 or PKCS#8), read and parsed at start. All three are required together. The server refuses to start when `domain` is the `base_url` host or a subdomain of it, because Go clients would send it their credentials: use a sibling host. The distribution's cache policy must key on the path plus `response-content-disposition` and `response-content-type` only. Allowed under `blob` and `blob.stores.<name>`, refused under `cold_blob` |
| `blob.s3.cdn.cloudfront.hash` | | | `sha1` | The signature hash, `sha1` or `sha256` |
| `blob.s3.cdn.cloudfront.path_prefix` | | | empty | The distribution origin's path inside the bucket, when the origin is not the bucket root. It must contain `blob.s3.prefix`, or the store refuses to start |
| `blob.azure.cdn.front_door.endpoint` | | `ARTIFACTWEAVE_BLOB_AZURE_CDN_FRONT_DOOR_ENDPOINT` | unset | An Azure Front Door endpoint whose origin is the storage account (decision [0029](../research/decisions/0029-cdn-delivery-and-download-redirect.md)). A repository with `cdnRedirect` or `downloadRedirect` then answers downloads with a 302 to the endpoint carrying a read SAS, unless the client sends `X-JFrog-Download-Redirect-To: S3` or `None`. Front Door checks no signature: it passes the SAS to storage, which checks it, so the store's credential must sign one (`shared_key`, a connection string with a key, or Entra ID); the server refuses to start on `sas`. The SAS is fixed to `download.redirect.ttl`-long windows, so every client in a window gets the same URL and the edge's cache serves it; it expires at the window's end plus the TTL, and sets the response's `Cache-Control` to `public, max-age=<TTL>`. The route must set `query_string_caching_behavior` to `UseQueryString`, or a cached private blob is served to anyone who asks for its path, and must cap cache expiry at the TTL: Front Door keeps serving a cached copy after its SAS expires, so a URL can reach a blob for up to twice the TTL plus that cap. With Entra ID a SAS cannot outlive its user delegation key, so the TTL must leave twice it under 7 days less an hour. `endpoint` is the endpoint host, or an `http://` URL for a test edge; the same sibling-host rule as CloudFront applies. Allowed under `blob` and `blob.stores.<name>`, refused under `cold_blob` |
| `blob.azure.cdn.front_door.path_prefix` | | `ARTIFACTWEAVE_BLOB_AZURE_CDN_FRONT_DOOR_PATH_PREFIX` | empty | The route's origin path, starting with the container, when the endpoint path leaves part of `<container>/<blob name>` out. It must contain `<container>/<blob.azure.prefix>`, or the store refuses to start |
| `blob.stores.<name>.*` | | `ARTIFACTWEAVE_BLOB_STORES_<NAME>_*` for stores the file declares | | A named store: `backend` (`fs`, `s3`, `gcs` or `azure`) and that backend's keys as above, `zone` (a region or site label) and `write_enabled` (default `true`; `false` drains the store: no new copies, reads continue, and repair copies blobs whose only copies are on it elsewhere). Remove a member from `blob.mirror.members` only once its `soleCopies` in `GET /api/storageinfo` is 0: the mirror never reads from a member that is not configured (decision 0028). Names are 1 to 32 of `a-z`, `0-9` and `_`. An `fs` store without `fs.root` uses `<data_dir>/blob-stores/<name>`, so no two stores share a directory |
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

## First start and authentication

The first start against an empty store creates the principals Artifactory ships, once
(decision 0005):

| Principal | What it is |
|---|---|
| `admin` | Administrator, with `bootstrap.admin_password` or the generated password |
| `anonymous` | What a request without credentials acts as when anonymous access is on; it has no password |
| `readers` | Group that new users join automatically |
| `Anything` | Permission: `readers` may read every repository |
| `Any Remote` | Permission on every remote repository that grants no one, as Artifactory ships it since 7.84.3 ([permissions](https://docs.jfrog.com/administration/docs/permissions)). Add users or groups to it, or write a permission of its own, to let them read remote repositories |
| `artifactory-build-info` | Local repository for build-info |

Later starts recreate none of them, so a principal an administrator removed stays removed.
Changing `bootstrap.admin_password` after the first start has no effect.

Neither permission grants `anonymous`. With anonymous access on, a request without
credentials reads only what a permission naming `anonymous` grants, so give `anonymous` a
permission of its own on the repositories it should read.

Requests authenticate with HTTP Basic, using a password or an access or reference token as
the password; with `Authorization: Bearer` and an access or reference token; or with
`X-JFrog-Art-Api` and a reference token (API keys are retired). Wrong credentials answer
`401` on every route. A correct password that has expired answers `403` "Password has
expired" on every route but the user's own password change. From the second wrong password in a row for one user name, Basic
logins with that name answer `403` for a second, the right password included, and the
delay doubles with each further failure up to a minute; tokens are not affected. Each node
keeps this state in memory. The Access user lock policy
(`/access/api/v1/config/security/user_lock_policy`) tunes it with
`clientLoginSuspensionEnabled`, `maxLoginDelayIncorrectAttempts`, `baseLoginDelayMillis`
and `maxLoginDelayMillis`. A password creates no token through `POST /access/api/v1/tokens`
until `token.allow_basic_auth` is on; the legacy `POST /artifactory/api/security/token`
takes one, so the admin's first token can come from there.
Without credentials, only the probes (`system/ping`, readiness, liveness, router health),
the Access version and the usage sinks answer, until anonymous access is turned on. The
Artifactory version (`/artifactory/api/system/version`) is not among them; the JFrog CLI
and the Terraform providers send their credentials with it.

## Configuration as code

Repositories, proxies, property sets and layouts are declared in Artifactory's YAML
descriptor format (decision 0009). The same code applies it in three places:

- `PATCH /artifactory/api/system/configuration` with an admin's credentials, for pipelines
  and the `artifactory_*` configuration resources.
- `artifactweave config apply <file>`, which writes to the store the application
  configuration names, as `migrate` does. It needs the master key (descriptors carry
  passwords, which are sealed) but no admin credentials, and it can prepare a store before
  the first start. `--dry-run` checks the whole file against the current state and prints
  what would change without writing anything. It exits 1 on any error, naming the entry.
- `bootstrap.descriptor`, applied once after the first start's principals are seeded. A
  marker records the file and its SHA-256, so a later start never re-applies it, even if
  the file changed. A descriptor that cannot be applied stops the start.

The descriptor is a patch:

- Named entries that do not exist are created; existing ones are updated field by field,
  omitted keys are untouched, and `key: null` deletes the entry. Inside a repository entry,
  `null` clears a string field; on `type`, on a block such as `xray` and on a field that is
  not a string it is refused (`400`), as Artifactory documents.
- An empty document is refused (`400`); Artifactory reads it as "delete every repository".
- Repository entries use the YAML spellings of JFrog's samples (`type`, with `terraformbe`
  for `terraformbackend`; `repoLayout`; `remoteRepoLayout`; `primaryKeyPairDescriptor`; and
  the `xray`, `downloadRedirect`, `helm`, `nuget`, `vcs`, `externalDependencies`, `bower`,
  `cocoaPods`, `composer`, `pypi`, `p2` and `virtualCacheConfig` blocks) or the JSON API's
  field names, and are checked with the same rules as the repositories API. An unknown
  field is refused. `cocoaPods.specRepoProvider` is accepted and not stored.
- Values written as `${NAME}` are read from the environment of the process applying the
  descriptor; an unset variable is an error, not an empty value.
- The whole patch is checked first and written in one transaction, so a descriptor with
  one bad entry changes nothing. An entry that would not change is not written, so applying
  the same descriptor again reports everything as unchanged.

| Section | Applied as |
|---|---|
| `localRepositories`, `remoteRepositories`, `virtualRepositories` | Repositories. A virtual repository may list no members, or members created by the same patch; each must have the virtual repository's package type unless it is `generic`, with `maven`, `gradle`, `ivy`, `sbt` and `p2` counted as one. Deleting a repository removes it from the virtual repositories that list it |
| `proxies` | Proxy settings; `password` is sealed and never stored in the document |
| `propertySets` | Property sets |
| `repoLayouts` | Repository layouts, checked by compiling `artifactPathPattern` and the descriptor pattern. An entry may repeat its key as `name`, as the `artifactory_repository_layout` resource sends it; a different `name` is refused, so delete a layout and create it under the new key to rename it |
| `downloadRedirectConfig` | One mapping, not entries by name: `fileMinimumSize`, the smallest file in whole MB that a repository with `downloadRedirect` or `cdnRedirect` redirects, Artifactory's "Minimum Direct Cloud Storage Download Size". It takes precedence over `download.redirect.min_size` from the next download on. It cannot be set to null; set it to the size wanted instead |

Any other section, such as `security`, `backups`, `mailServer` or
`federatedRepositories`, is refused (`400`), naming it, and nothing in the descriptor is
applied. Each section is accepted once its feature lands. Artifactory's own bootstrap file,
`artifactory.config.import.yml`, is a different format and is refused the same way.

The PATCH answers Artifactory's `<n> changes to config merged successfully`, where `<n>`
counts the values in the document (each scalar, null and list element), changed or not.
`config apply` prints a summary naming the entries created, updated, deleted and unchanged.

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

