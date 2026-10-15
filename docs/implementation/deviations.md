# Deviations from Artifactory

artifactweave mirrors the JFrog API surface ([0001](../research/decisions/0001-mirror-the-jfrog-api-surface.md))
so the JFrog CLI, the Terraform providers and the native package clients work unchanged.
It is built with Artifactory's history in view, and where Artifactory made a mistake or hit
a limit we needn't repeat, artifactweave deliberately does something else. This register
lists every such departure in one place.

## Scope

- **A deviation** is a deliberate departure from Artifactory's behaviour or design.
  Wire compatibility is kept unless the entry's client impact says otherwise.
- **A deviation that would break the JFrog CLI or a Terraform provider** needs a decision
  record first. The entry then links to it.
- **Documentation versus behaviour** widens the scope: it lists the places where
  artifactweave follows Artifactory's observed behaviour over JFrog's documentation, because
  a reader comparing artifactweave with the docs would otherwise take them for deviations.
- **Not listed here:** features cut from v1 and endpoints answered by a stub. Those are in
  [research 10](../research/10-scope-and-tiers.md) and
  [research 12](../research/12-open-questions.md). Gap-filling choices where Artifactory's
  behaviour is undocumented stay in their decision records.

## Adding an entry

Add a row to the section that fits and give it the next free number. The decision record
that makes the choice links back here from its Rationale.

| Field | Content |
|---|---|
| ID | `DV-NNN`, numbered across the whole file and never reused |
| Artifactory | What Artifactory does, as documented or observed |
| artifactweave | What artifactweave does instead |
| Why | The mistake or limit avoided. "Reason not recorded" when no source gives one |
| Client impact | `none`, or what a client, an operator or a script sees differently |
| Source | The decision or research section that records it |

## Data model

| ID | Artifactory | artifactweave | Why | Client impact | Source |
|---|---|---|---|---|---|
| DV-001 | Keys its filestore by SHA-1 | Addresses blobs by SHA-256 and records SHA-1 and MD5 in the item index | SHA-1 is deprecated for integrity, and SHA-256 is what Docker and the JFrog CLI check | none: all three checksums are served | [0004 Rationale](../research/decisions/0004-storage-sql-metadata-and-blobs.md#rationale) |
| DV-002 | Shows a remote repository's cache as a `<repo>-cache` repository | Stores cache entries as items flagged `cached` under the remote's own key, and renders `<repo>-cache` at the API edge wherever Artifactory shows it | One item model; a real cache repository would leak into listings and could be created or changed by clients | none | [0007 Decision](../research/decisions/0007-remote-repositories.md#decision) |

## Security

| ID | Artifactory | artifactweave | Why | Client impact | Source |
|---|---|---|---|---|---|
| DV-003 | API keys are deprecated, and creating one is disabled by default from 7.98 | No API keys: creating one answers 400, looking one up answers 404, and `X-JFrog-Art-Api` accepts reference tokens only | Matches JFrog's own end of life and removes a weak credential | Scripts that create or send API keys must use reference or access tokens | [0005 Decision](../research/decisions/0005-authentication-and-tokens.md#decision), [research 05](../research/05-platform-identity-and-security-apis.md#legacy-artifactory-security-api-artifactoryapisecurity) |
| DV-004 | `GET /api/security/encryptedPassword` returns the user's password in encrypted form | Returns a non-expiring, revocable reference token bound to the caller, one per user, revoked whenever the password is set or expired | Implied by the source: passwords are kept only as Argon2id hashes, so there is no reversible form to encrypt | none: the value works as the Basic password wherever the encrypted password did | [0005 Decision](../research/decisions/0005-authentication-and-tokens.md#decision) |
| DV-005 | Documents `PUT /access/api/v2/users/{username}/password` as open to an administrator or the user named in the path, with no rule on token scope | A token narrowed to repositories or groups gets 403, as it does when minting tokens | Setting the password would turn a narrowed token into a credential with all of the user's rights (#84) | Narrowed tokens cannot change passwords; identity tokens and passwords can | [0005 Decision](../research/decisions/0005-authentication-and-tokens.md#decision) |
| DV-006 | A worker's `clients.axios` makes outbound HTTP calls; JFrog documents no host restriction | Outbound calls reach only hosts on an allow-list in the application configuration, empty by default, and never loopback, link-local or this instance except through `platformHttp` | Implied by the rule: a worker cannot reach internal or link-local services | A worker that calls an external service needs that host on the allow-list | [0026 Decision](../research/decisions/0026-workers-runtime.md#decision) |

## Hosting

| ID | Artifactory | artifactweave | Why | Client impact | Source |
|---|---|---|---|---|---|
| DV-007 | Gates features behind Pro, Enterprise and Enterprise+ licences | No licence tiers: every in-scope feature is available and nothing is gated | Reason not recorded | none | [0001 Decision](../research/decisions/0001-mirror-the-jfrog-api-surface.md#decision), [research 02](../research/02-artifactory-core-api-inventory.md#deprecated-and-licence-gated-endpoints) |
| DV-008 | Instances federate, distribute to Edge and share access through undocumented, licence-gated protocols and a Circle of Trust | Peers only with artifactweave, over its own protocol: Ed25519 keys exchanged at pairing and signed requests over TLS. The management APIs keep JFrog's paths and shapes | JFrog's protocols are undocumented, licence-gated to obtain and liable to change without notice | Management APIs are unchanged; an artifactweave instance cannot federate with Artifactory or Edge | [0021 Context](../research/decisions/0021-peer-trust-and-sync-protocol.md#context), [0021 Rationale](../research/decisions/0021-peer-trust-and-sync-protocol.md#rationale) |
| DV-009 | Offers three Docker access methods (repository path, subdomain, port per repository), each needing a reverse proxy or DNS | Routes `/v2/{repo}/...` natively on its own port with no reverse proxy; subdomain matching is optional; port per repository is not offered | Repository-path routing is the only method that works on one port with no infrastructure, so a single static binary must own it | Port-per-repository setups move to path routing; an image name cannot start with another repository's key | [0003 Decision](../research/decisions/0003-repository-model.md#decision), [0003 Rationale](../research/decisions/0003-repository-model.md#rationale) |
| DV-010 | Runs Workers in its own TypeScript sandbox | Transpiles TypeScript with esbuild and runs it on goja in helper processes of the same binary, one execution per helper, with JFrog's documented context and default limits | Keeps the single static binary, and one execution per helper makes the memory cap enforceable | none for workers written against JFrog's documented context | [0026 Decision](../research/decisions/0026-workers-runtime.md#decision), [research 16](../research/16-enterprise-products.md#5-workers) |
| DV-011 | Receives the JFrog CLI's usage reports (`POST /api/system/usage`) and jfconnect metrics | Answers both with 2xx and discards the body | Reason not recorded | none | [0005 Decision](../research/decisions/0005-authentication-and-tokens.md#decision) |

## Infrastructure and storage

| ID | Artifactory | artifactweave | Why | Client impact | Source |
|---|---|---|---|---|---|
| DV-012 | Shards across cloud buckets on S3 only; its GCS and Azure templates shard across nodes in front of one bucket | A mirror composes `fs`, `s3`, `gcs` and `azure` stores in any mix of clouds, regions and accounts | The blob contract is the same for every backend | none | [0028 Rationale](../research/decisions/0028-blob-mirroring.md#rationale), [research 18](../research/18-cloud-blob-storage.md#31-how-artifactory-does-it) |
| DV-013 | Does not know which shards hold a binary, so reads and repair probe every shard | Records each copy's member and state in the `blob_replicas` index | Reads go straight to a holder, and repair works from a queue | none | [0028 Rationale](../research/decisions/0028-blob-mirroring.md#rationale), [research 18](../research/18-cloud-blob-storage.md#31-how-artifactory-does-it) |
| DV-014 | Restored missing copies only in balancing runs after a full garbage collection, about every 80 hours by default, until 7.104.5 made `optimize` start one | A repair worker runs on its own schedule, and `POST /api/system/storage/optimize` queues a pass at once | Avoids a multi-day gap between a failure and restored redundancy | none | [0028 Decision](../research/decisions/0028-blob-mirroring.md#decision), [research 18](../research/18-cloud-blob-storage.md#31-how-artifactory-does-it) |
| DV-015 | On S3 shards, topology changes apply to new data only | Repair backfills a newly added member from the index | So existing data is covered | none | [0028 Decision](../research/decisions/0028-blob-mirroring.md#decision), [research 18](../research/18-cloud-blob-storage.md#31-how-artifactory-does-it) |
| DV-016 | Removes surplus copies automatically; combined with bucket replication this deleted data (KB 000006410) | Never deletes surplus copies automatically. Removing a member is an explicit drain, safe once its `soleCopies` is 0 | Avoids the documented data loss | Operators drain a member before removing it | [0028 Decision](../research/decisions/0028-blob-mirroring.md#decision), [research 18](../research/18-cloud-blob-storage.md#31-how-artifactory-does-it) |
| DV-017 | Fans one upload out to R writers and counts the write when all of them finish (`concurrentStreamWaitTimeout`, 30 s) | An upload succeeds once W members hold the blob; the others get `member_timeout` to finish, then become pending repairs | If every member had to succeed, any one outage would stop all uploads | none | [0028 Decision](../research/decisions/0028-blob-mirroring.md#decision), [0028 Rationale](../research/decisions/0028-blob-mirroring.md#rationale) |
| DV-018 | `lenientLimit` defaults to 0, meaning every one of the `redundancy` shards must be available for uploads to continue | `write_quorum` defaults to R-1, and at least 1 | So one member down does not stop uploads | A default mirror keeps accepting uploads with one member down | [`internal/config/blob.go`](../../internal/config/blob.go) (`BlobMirrorConfig`), [research 18](../research/18-cloud-blob-storage.md#31-how-artifactory-does-it) |
| DV-019 | Archives to a separate cold Artifactory instance (self-hosted Enterprise X and Enterprise+), or through smart archiving in SaaS | Archives to a `cold_blob` store inside the same instance, keeping each archived item's metadata in the hot database; the `artifactory-cold` pairing use case answers 404 | The cost saving without operating a second registry; changing storage classes in place would tie archiving to S3 | The archive APIs keep JFrog's shapes; there is no cold instance to pair | [0024 Context](../research/decisions/0024-cold-storage-tier.md#context), [0024 Rationale](../research/decisions/0024-cold-storage-tier.md#rationale) |

## Delivery

| ID | Artifactory | artifactweave | Why | Client impact | Source |
|---|---|---|---|---|---|
| DV-020 | Gives the JFrog CLI presigned S3 URLs for multipart upload parts, and the object store assembles them | Gives out its own part URLs, signed with HMAC for one upload and one part, and appends the parts to one blob upload session | Every backend accepts multipart uploads, the mirror included | none: the CLI uploads each part to the URL it is given | [research 09](../research/09-jfrog-cli-compatibility.md#multipart-upload) |
| DV-021 | Signed URLs need an Enterprise+ or Edge licence, and direct download needs `enableSignedUrlRedirect` on the binary provider; the signed URL lives `signedUrlExpirySeconds`, 30 s by default | A repository's `downloadRedirect` is enough on any store that can sign: s3, gcs, azure, and a mirror through the first member in read order that holds the blob and signs. The URL lives `download.redirect.ttl`, 5 minutes by default. A store that cannot sign streams the download | Artifactory checks the expiry when a request arrives, so after 30 s a client retrying a dropped connection or fetching a later range of a split download fails without coming back to the registry. 5 minutes covers those retries and a leaked link still expires quickly | Signed links stay valid for 5 minutes rather than 30 seconds, unless the TTL is set lower | [0029 Rationale](../research/decisions/0029-cdn-delivery-and-download-redirect.md#rationale), [research 19](../research/19-cdn-and-download-redirect.md#11-direct-cloud-storage-download-downloadredirect) |
| DV-022 | CDN delivery is CloudFront only: `cdnRedirect` on JFrog SaaS with an Enterprise X or Enterprise+ licence, or CloudFront on the self-hosted S3 provider | Each store can have a CDN: CloudFront on s3 (#120), Cloud CDN on gcs (#121) and Front Door on azure (#122), on any instance with no licence tier. `downloadRedirect` or `cdnRedirect` sends downloads to the store's CDN when it has one, as self-hosted Artifactory does with CloudFront | Teams leaving Artifactory run on all three clouds and host their own instances | none: the flags and the redirect header keep their meaning | [0029 Decision](../research/decisions/0029-cdn-delivery-and-download-redirect.md#decision), [research 19](../research/19-cdn-and-download-redirect.md#13-cdn-distribution-cdnredirect) |
| DV-029 | `X-JFrog-Download-Redirect-To: S3` sends the download to S3 even when CloudFront exists; the header applies to self-hosted S3 stores only | `S3` sends the download to the object store, not the CDN, on every cloud: S3, GCS or Azure. `None` and `CF` keep their meaning | One header for every backend; the value names the route, not the vendor | none for S3 stores; on GCS and Azure, `S3` means that store's own URL | [0029 Decision](../research/decisions/0029-cdn-delivery-and-download-redirect.md#decision), [research 19](../research/19-cdn-and-download-redirect.md#14-the-redirect-request-header) |

## API behaviour

| ID | Artifactory | artifactweave | Why | Client impact | Source |
|---|---|---|---|---|---|
| DV-023 | Reads an empty YAML descriptor patch as "delete every repository" | Refuses an empty document with 400; deleting needs `null` per element | Implied by the source: an empty patch is destructive | A pipeline that relied on an empty patch to delete everything gets 400 | [0009 Constraints](../research/decisions/0009-configuration-as-code.md#constraints), [configuration reference](../operations/configuration-reference.md#configuration-as-code) |
| DV-024 | `GET /api/system/license` returns the instance's licence | Answers `type: "artifactweave"` | Implied by the source: there is no JFrog licence to report, and no client gates on it | none: the JFrog CLI and the providers never check the licence | [0005 Decision](../research/decisions/0005-authentication-and-tokens.md#decision) |
| DV-025 | Accepts a federated repository's member URLs as given | A member URL whose host is not a peer paired for federation answers 400 naming the URL | Turns a typo into a create-time error instead of a silent member that never syncs | Peers must be paired before they are listed as members | [0022 Decision](../research/decisions/0022-federated-repositories.md#decision), [0022 Rationale](../research/decisions/0022-federated-repositories.md#rationale) |
| DV-030 | Self-managed Artifactory has no default AQL result cap; `aql.search.query.max.limit` can set one from 7.90.6. JFrog SaaS caps at 500,000 | `search.aql.max_results` caps every AQL query at 500,000 results by default, adding the documented `range.notification` when it trims; 0 removes the cap | An unbounded query can exhaust the server's memory; 500,000 is the value JFrog runs its own SaaS with | A query that would return more than 500,000 rows is trimmed, with `range.notification` saying so, unless the operator raises or removes the cap | [0012 Rationale](../research/decisions/0012-search-and-aql.md#rationale), [research 22](../research/22-aql-searches-build-info.md#12-output-sorting-and-limits) |

## Operations

| ID | Artifactory | artifactweave | Why | Client impact | Source |
|---|---|---|---|---|---|
| DV-026 | `GET /api/storageinfo` has no per-shard detail | Adds `blobMirrors` for each mirrored tier: each member's replicas by state, availability, latency and `soleCopies`, the under-replicated count, and removed members | Shows when a drained member is safe to remove | none: JFrog clients ignore the extra field | [0028 Decision](../research/decisions/0028-blob-mirroring.md#decision), [research 18](../research/18-cloud-blob-storage.md#31-how-artifactory-does-it) |

## Documentation versus behaviour

These are not departures from Artifactory. They are places where JFrog's documentation and
Artifactory disagree and artifactweave follows Artifactory.

| ID | Artifactory | artifactweave | Why | Client impact | Source |
|---|---|---|---|---|---|
| DV-027 | Its docs and OpenAPI type `enableFileListsIndexing`, `downloadRedirect`, `cdnRedirect`, `priorityResolution` and `blockPushingSchema1` as strings on local and federated repositories, and federated `members[].enabled` as a string; Artifactory sends JSON booleans | Writes booleans for every class and accepts both spellings on input | The Terraform provider decodes them as booleans; echoing strings broke its refresh (#69, #109) | none | [research 03](../research/03-repository-configuration-model.md#typing-traps) |
| DV-028 | Its docs show `passwordExpirationPolicy` values as quoted strings; the Terraform provider decodes `bool` and `int64` | Emits real JSON booleans and integers and accepts string spellings on write | The provider round-trips the policy | none | [research 05](../research/05-platform-identity-and-security-apis.md#legacy-artifactory-security-api-artifactoryapisecurity), [0005 Decision](../research/decisions/0005-authentication-and-tokens.md#decision) |
| DV-031 | Its Builds API page documents `POST /api/build/retention/{name}` as answering 200, and types `count` as a number; the JFrog CLI accepts only 204 and sends `count` as a string | Answers 204 and accepts `count` as a string or a number | `jf rt build-discard` fails on any other status and sends the string form | none | [0014 Decision](../research/decisions/0014-build-info.md#decision), [research 22](../research/22-aql-searches-build-info.md#31-the-api) |
