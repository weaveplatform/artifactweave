# 0028: Mirroring blobs across stores, clouds and regions

Status: Proposed

## Context

A single blob store is a single point of failure, and one region or one cloud provider
can be lost.

Artifactory keeps several copies of each binary with its `sharding` binary provider:
- R copies over M storage units (`redundancy`);
- a minimum of available units below which uploads stop (`lenientLimit`);
- zone-aware reads and writes;
- a background balancer that restores missing copies.

Its cloud sharding is S3-only. It keeps no record of which shard holds a binary. Its
repair ran only after garbage collection until 7.104.5. Its automatic removal of surplus
copies has deleted data ([research 18](../18-cloud-blob-storage.md) section 3.1).

Cloud-native replication (S3 cross-region replication, GCS dual-region, Azure GRS and
object replication) is asynchronous and never crosses clouds (section 3.2).

## Decision

**Members.**
- `blob.backend: mirror` composes named blob stores (`blob.stores.<name>`), each an `fs`,
  `s3`, `gcs` or `azure` store with its own credentials and a `zone` (for example
  `aws-eu-west-1`, `gcp-europe-west4`).
- A member's own native replication, such as a dual-region bucket, is invisible to the
  mirror and never counts as one of its copies.

**Settings.**

| Setting | Meaning | Artifactory equivalent |
|---|---|---|
| `members` | the stores that hold copies | sub-providers |
| `redundancy` R | copies kept of each blob; default every member, a full mirror; R < M places copies by write order | `redundancy` |
| `write_quorum` W | copies that must be stored before an upload succeeds, 1 <= W <= R; default R-1 (at least 1), so one member down does not stop uploads | `lenientLimit` |
| `zone` | this instance's zone; reads and writes prefer members in it | `crossZoneOrder` |
| `member_timeout` | how long a straggling member may finish after the quorum before its copy is left to repair | `concurrentStreamWaitTimeout` |
| member `write_enabled` | false drains a member: no new copies, reads continue | `writeEnabled` |

The server refuses to start unless W <= R <= M.

**Writes.**
- An upload session opens a session on each target member.
- Each `Append` hands one immutable part-sized buffer to every member's `Append` in
  parallel, each with its own context. A member whose stored size diverges from the
  session's is dropped from that session.
- `Commit` commits every target in parallel and succeeds once W members hold the blob.
  Members still running get `member_timeout` to finish in the background; any that miss
  it, or failed, become pending replicas.
- Every copy's state is recorded in the `blob_replicas` index (sha256, member, state).
- A retried commit after a partial success treats "session gone, blob present" as
  committed.

**Reads.**
- `Stat` and `Open` consult the index, then try members in read order: this zone first,
  then healthy members by observed latency.
- A failure part-way through a read reopens on the next member and seeks to the same
  offset, which is safe because blobs are immutable.
- Presigned URLs come from the first healthy member in read order that can sign.

**Health.** Each member has a circuit breaker: consecutive failures open it, and a timed
re-check half-opens it. An open member is skipped for reads and treated as down for
writes.

**Repair.**
- A worker under a named lease copies pending replicas from a member that holds the blob,
  with backoff.
- It backfills a newly added member from the index, so existing data is covered.
- It drains a member set to `write_enabled: false`.
- An immediate pass can be triggered (the optimize endpoint).

**Deletes.**
- `Delete` removes every copy. A copy that cannot be removed is kept as a tombstone and
  retried later, and the retry re-checks the blob's reference count so a re-uploaded blob
  is never deleted.
- Surplus copies are never deleted automatically; removing a member is an explicit job
  that runs only after the remaining copies are verified.

**Removing a member.** Repair works only with configured members. The records of a member
taken out of `blob.mirror.members` stay in the index, but repair neither copies to that
member nor reads from it, and backfill ignores its copies. A blob whose only present copy
is there is out of the mirror's reach until the member is configured again. To remove one
safely:
1. Set `write_enabled: false`. Repair then drains it: blobs whose only copies are on it
   get pending copies on write-enabled members.
2. Wait until its `soleCopies` in `GET /api/storageinfo` is 0, and its pending records
   are gone, or run `POST /api/system/storage/optimize` to hurry repair.
3. Remove it from `blob.mirror.members`. Its records then appear under `removedMembers`
   until they are deleted; its stored data is not touched.

**Administration.** Artifactory's storage summary has nothing per shard. artifactweave's
adds one field, which JFrog clients ignore:
- **`GET /api/storageinfo`** gains `blobMirrors` when a tier is a mirror. A summary without
  mirrors is unchanged. There is one entry per mirrored tier (`blob`, `cold_blob`), with:
  - `tier`, `zone`, `redundancy` and `writeQuorum`;
  - `underReplicatedBlobs`: blobs with fewer present copies than the redundancy, not
    counting blobs being deleted;
  - `members`, each with `name`, `zone`, `backend`, `writeEnabled`, `available` (false
    while its breaker is open) and `averageLatencyMs`;
  - each member's `replicas`, keyed by state (`present`, `pending`, `deleting`,
    `failed`), each with `count`, `bytes` and a human-formatted `size`;
  - each member's `soleCopies`: blobs whose only present copy is on it, so a drained
    member is safe to remove when this is 0;
  - `removedMembers`: members with replica records that are no longer configured, each
    with its `replicas` and `soleCopies`. Their sole copies are blobs the mirror can no
    longer read (#71).
- **`POST /api/system/storage/optimize`** (admin) queues a repair pass on every mirror at
  once and answers `200` "Blob mirror repair started". A pass already queued counts.
  Without a mirror it answers `200` "No blob mirror is configured; nothing to optimize".
  The spec documents only a plain-text acknowledgement, so both texts are
  artifactweave's.

## Rationale

**What carries over from Artifactory:** quorum writes with background repair keep
uploads working through a single cloud or region outage, the reason operators deploy
sharding.

**Where it improves on Artifactory:**
- **A copy index** (`blob_replicas`) means reads go straight to a holder and repair works
  from a queue instead of probing every member.
- **Repair on its own schedule** avoids Artifactory's multi-day gap between a failure and
  restored redundancy.
- **Any mix of clouds and regions:** the blob contract is the same for every backend, so
  one mirror can mix them, which Artifactory cannot.
- **No automatic deletion of copies,** to avoid the documented data loss.

**Chunk-level fan-out** keeps one upload's memory bounded by one chunk, and lets each
member's session resume independently. The chunk is the smallest multiple of every member's
part size that is at least 8 MiB. A chunk that left a member a partial part would keep those
bytes pending in its session, and a durable session writes them to the database after every
chunk. The s3 and azure defaults (8 MiB) with the gcs default (16 MiB) give a 16 MiB chunk.
Part sizes that need a chunk above 256 MiB stop the mirror from starting (issue #39).

**Alternatives considered:**
- *All members must succeed:* the strongest guarantee, but any one outage stops all
  uploads.
- *Primary plus asynchronous copies:* fastest uploads, but the primary is a single point
  of failure and copies lag.
- *Native cloud replication only:* asynchronous, single-cloud, and invisible to our index.

## Constraints

**Cost and latency.**
- Writes and repairs across clouds or regions pay egress.
- A quorum write is as slow as the W-th fastest member.

**Presigned URLs.** They bypass the mirror's health checks once handed out. A member that
fails after signing serves errors until the URL expires.

**Configuration.** Member lists are set in the configuration file only. Environment
variables can override keys of members the file declares. Credentials come from each
member's SDK credential chain or a named profile, never from the file.

**Partial walks.** A `Walk` while a member is down is partial, so garbage collection must
never decide from it alone.

## Verification

- `blobtest.Run` against mirrors of fakes, of `fs` and of the s3 fake.
- Failure scenarios, with timing under `testing/synctest`:
  - a member down at append and at commit;
  - a quorum not met;
  - a retried partial commit;
  - the breaker opening and half-opening;
  - a deferred delete racing a re-upload.
- On Linux CI, a three-member mirror of MinIO, fake-gcs-server and Azurite: it uploads
  with a member stopped, repairs after a restart, and fails over on read.

## References

- [Research 18](../18-cloud-blob-storage.md), section 3: Artifactory sharding parameters,
  cloud-native replication, Go concerns, with sources.
- Decision [0027](0027-cloud-blob-backends.md) (the member backends) and
  [0004](0004-storage-sql-metadata-and-blobs.md) (the blob contract).
