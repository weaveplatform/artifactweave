# P9 remote cache maintenance

Implementation checkpoint for [#622](https://github.com/weaveplatform/artifactweave/issues/622),
2026-10-21. This implements the maintenance part of the
[retrieval contract](remote-retrieval-contract.md); adapter-specific remote resolution
continues in #623–#633. [ADR0007](../research/decisions/0007-remote-repositories.md)
remains Proposed. General package cleanup policies belong to P11.

## Expiry and removal

| Operation | Durable effect | Bytes and next read |
|---|---|---|
| Metadata zap | Advance matching metadata/negative/empty checkpoint epochs; clear validation and confirmed misses. Preserve validators attached to usable raw metadata for conditional refresh. | Keep raw metadata and both unique and mutable binaries. A normal metadata read attempts refresh and applies the shared stale/failure policy. A cache alias never fetches. |
| Explicit cached path/subtree deletion | Invalidate all matching representations and pending completions in the same transaction as item deletion, index pruning and blob-reference release. | Remove item-backed and raw bodies; clear negative-only entries even without an Item row. A later normal read may fetch again. Remote cache deletion bypasses trash. |
| Repository source edit/delete/recreate | The store changes source generation or repository incarnation; outstanding publications cannot use their old fences. Policy-only edits fence publication separately. | Old source bytes cannot become a normal-read hit for the new source. Existing cache-only item access remains governed by current repository authorization. |
| Restore into a remote cache | Advance path epochs before restoring the retained item reference, properties and format hooks. Join the caller's trash transaction. | Restored bytes have no invented upstream validation time. A late pre-restore fetch cannot overwrite them. Normal retrieval must establish source validity again. |
| Reindex | Invoke the format's local index rebuild. | Derived views remain separate from raw upstream bodies/validators. A local cached subset must never replace the authoritative upstream version list. |

The public [Zap Cache API](https://docs.jfrog.com/artifactory/reference/zapCache)
documents metadata expiry, immutable binary preservation, refresh before the next
normal response and stale fallback when the upstream is unavailable. Manage **or**
Delete authorization and the caller's path scope apply to zap. Delete requires Delete;
format-owned sidecars retain the existing authorization checks on every affected path.
Background cleanup acts only on server-owned cached content, not as a user deployment.

A deletion of a negative, pending or already-empty durable checkpoint is idempotent;
a truly unknown path keeps the existing not-found response. Epoch tombstones remain
after removal so a delayed response cannot recreate deleted state. Exact vendor
responses for repeated tombstone-only deletion remain [I], tracked with RC-4/#216.
No operation treats a HEAD response or partially downloaded body as usable content.

## Unused cleanup

The [maintenance guide](https://docs.jfrog.com/administration/docs/regular-maintenance-operations)
distinguishes an artifact's unused-age eligibility from the scheduled deletion pass.
`unusedArtifactsCleanupPeriodHours=0` disables this cleanup, including work already
queued before the change. A positive value enables an hourly scheduler and persistent
per-repository jobs. This cadence and the precise clock/exclusions below are
implementation decisions [I], not claimed observations of Artifactory 7.161.32.

Each job records the repository incarnation and a keyset cursor. It scans retrieval
entries, then cached items, in pages of at most 128. Pages requeue immediately rather
than waiting another hour; restarting preserves the cursor. The scheduler never
replaces a pending cursor. The shared work queue retries faults with bounded backoff
and parks a repeatedly failing job; parked jobs remain visible for operator recovery.
An old job cannot delete a recreated repository's content. Disabling cleanup is
rechecked at job entry, before each deletion and before queueing another page.

For item-backed content, activity is the maximum of successful
`ItemStats.LastDownloaded`, every representation's `LastUsed`, and `FetchedAt`.
Legacy items without retrieval state use their creation time as the initial baseline.
Raw metadata uses `max(LastUsed, FetchedAt)`. Unknown or future activity is retained.
Downloads/usage touches do not mark content as freshly validated. Item/source locks
serialize cleanup's final usage check with publication and successful download
accounting; a newer checksum or a newly used representation wins over a scan snapshot.
Representations of one item path are read in bounded pages so later variants' activity
is not ignored. This bounds memory; a path with many variants may need multiple reads
within its deletion transaction.

Unused raw metadata releases its independent blob reference. Expired negatives are
cleared independently of artifact age. Empty path checkpoints keep their epochs.
Folders and format-derived indexes are not age-evicted. Artifact removal uses the
ordinary item service: deletion hooks, owned sidecars, package-index pruning and blob
reference updates commit together. A recently used owned sidecar protects its owner
from a grouped deletion. A sidecar explicitly selected as its own old cached item is
otherwise independently eligible, as with a direct cache deletion. No local-content,
trash or build-retention policy is implied by this worker. Unreferenced content is
collected by the existing blob GC, not deleted from storage inside this transaction.

## Format ownership

These are maintenance boundaries, not a claim that every remote adapter is complete.

| Format family | Maintained content and index boundary | Remote adapter owner |
|---|---|---|
| Generic | Complete cached files, checksum sidecars and successful download statistics; no invented package metadata. | #621 |
| Maven | Cached artifacts/snapshots keep their retrieval kind; local derived metadata and package links use existing hooks. Raw upstream version metadata remains separate. Remote snapshot retention integration stays with the adapter. | #625 |
| npm, PyPI, NuGet, Go | Cached package files use item/index deletion hooks. Raw upstream version/service documents cannot be replaced by a locally cached subset. Native discovery refresh and generated views require adapter tests. | #626–#629 and #633 |
| Helm | Chart deletion carries stored provenance sidecars; recent provenance usage protects grouped deletion. Derived index regeneration cannot overwrite the raw upstream index. | #630 |
| Docker/OCI | Generic cleanup does not assert reachability-aware layer or manifest retention. The adapter must preserve manifest/blob references and tag rules; local push-retention settings do not become remote pull cleanup. | #623–#624 |
| Terraform registry | Module/provider files use existing item/blob hooks. Signed provider checksums/signatures and download URLs remain under registry adapter rules. Terraform backend state is a different repository format. | #631–#632 |
| Other formats | Adopt the same raw/derived, activity, deletion and signature boundaries when their adapters land. | P28–P33 |

## Verification and capture gaps

Store contracts cover exact-entry eviction and independent variant references across
reference, SQLite and PostgreSQL stores. Item tests cover metadata-only zap, pending
publication fencing, delete/restore rollback, raw/derived separation, index pruning,
sidecar activity, current-item replacement checks, expired/fresh negatives, zero
configuration, bounded batches and SQLite restart. Database concurrency tests hold a
successful download transaction open while cleanup attempts the same item. HTTP tests
cover Manage-or-Delete, path restrictions, cache aliases and negative-only deletion;
`@cachemaintenance` exercises those operations and successful-download preservation
against the running server.

The shared retrieval-policy integration tests additionally prove changed metadata
refresh, permitted stale fallback versus hard failure, binary preservation and blocked
fetch/refresh races against service zap, delete, restore and repository recreation.
HTTP routing wired to that policy proves negative deletion discovers a newly available
upstream artifact while denied deletion and cache aliases perform no upstream I/O.
Their fake upstreams establish our behavior, not vendor parity. Extend RC-1/RC-4 on [#216](https://github.com/weaveplatform/artifactweave/issues/216)
with unused-clock behavior for successful GET/HEAD/range, raw metadata, sidecars,
zero-to-positive edits, job restart and concurrent reads; repeated tombstone deletion;
and reindex/restore while a fetch is blocked. P19 owns live capture of these [I] details.
