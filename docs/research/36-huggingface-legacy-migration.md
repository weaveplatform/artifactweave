# Bounded Hugging Face legacy layout migration

Reviewed 2026-11-03 for #789 against the published JFrog guide and the existing
Hugging Face Hub 2.3.0 / hf-xet 1.7.0 implementation.

The [JFrog repository guide](https://docs.jfrog.com/artifactory/docs/hugging-face-repositories)
describes legacy local paths as `models/<owner>/<name>/<ref>/<timestamp>/<file>`
and the analogous dataset layout. Historical revision identity is retained in
`huggingfaceml.generated.revision.sha1` on the internal information item. Current
paths use `models--<owner>--<name>/snapshots/<revision>/<file>`. Its version
statements differ between 7.111.1 and 7.111.11; this implementation identifies
the path grammar, rather than inferring a server version. The guide describes
an administrator upgrade UI but does not publish its request or metadata JSON
schema. Xet is supported only with the current layout. Federated upgrades
require coordinated peers and paused synchronization.

The [earlier GA migration note](https://jfrog.com/help/r/artifactory-hugging-face-ga-repository-structure-migration)
distinguishes pre-7.77 paths without timestamps from the timestamp layout and
recommends preserving local data through download/re-upload while omitting
internal metadata. Those beta paths and remote cache migrations are outside
this bounded operation.

## Implementation decision

The artifactweave-only operation explicitly names one source timestamp and its
40-hex revision. It verifies the documented item property without guessing the
internal JSON schema. An import lacking that property must first be reconciled
against an authoritative source export; the migration refuses to invent it.

All source files remain in place. The service verifies stored sizes and every
available checksum by streaming the blobs. Within the existing item transaction,
it copies content-addressed references through the normal deployment lifecycle,
preserves artifact properties and declared checksums, links the package/version,
and creates an optional named ref. There is no full-repository reindex or
unbounded job queue. A per-file source marker distinguishes an identical rerun
from unrelated existing content. An error rolls back every destination item,
index link and ref created by the request.

Each permission's target union must grant exactly the same paths before and
after mapping, including include/exclude patterns, file paths and native model
and revision folders. Non-equivalent policy is refused; the administrator must
review both layouts explicitly. Repository patterns are checked too. Source
rows are locked, and source/configuration/policy are rechecked after streaming
and policy again before transaction completion. Operators must quiesce policy
changes and repository writes during migration: this operation does not add a
global lock for concurrent administrator policy edits.

The caller must independently have administrative authority, source READ
(including revision metadata) and destination/ref READ plus DEPLOY. Token path
scopes apply to plans, publication and completed status alike. Permission target
matching delegates to the existing authorizer, including its normalization of
empty and whitespace patterns. Materialized bytes are required; an LFS v1
pointer header is refused even when the pointer's own stored checksum is valid.

No legacy Xet encoding is invented. Legacy large-file payloads preserve their
SHA256/LFS identity. Existing current-layout Xet records stay in place and retain
their model identity; absent records are not advertised as CAS-backed files.

See the [operator procedure](../operations/huggingface-migration.md) for bounds,
dry-run status, recovery, token scopes and unsupported cases. The required CI
native test downloads two migrated revisions with fresh caches using the pinned
Hub client. It seeds Xet using a real file-backed upload, verifies those records
are unchanged, requires CAS reads with HTTP fallback blocked, and checks a denied
native principal. Go tests cover policy refusal, conflicts, interruption and
repeatability.
