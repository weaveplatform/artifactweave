# 0023: Release bundles v1 and Distribution

Status: Proposed

## Context

JFrog Distribution creates signed, immutable release bundles v1 from AQL queries and pushes
them to Edge nodes chosen by distribution rules; Artifactory keeps a view of stored and
received bundles and a retention service for bundle versions
([research 16](../16-enterprise-products.md#1-distribution-and-release-bundles-v1)). The
owner brought Distribution and release bundles v1 into scope on 2026-10-13, with edges
being artifactweave instances ([0021](0021-peer-trust-and-sync-protocol.md)). Release
bundles v2 and the Lifecycle service stay out of scope, but the Distribution API also
exposes v2 distribution endpoints.

## Decision

**Bundles.** A release bundle version is a record identified by `(name, version)` holding
the creation spec (queries with AQL, path mappings, added properties and excluded property
patterns), the resolved artifact list (source path, target path, sha256, properties), the
description, release notes, storing repository, state and audit fields. Creation resolves
the queries through the AQL engine ([0012](0012-search-and-aql.md)) once; the artifact
list is then frozen. `dry_run` resolves and returns the list without storing. Updates are
allowed only before signing. Deleting a version removes it; the `archive` flag JFrog
accepts on that delete is accepted and ignored, because artifactweave has no bundle
archive separate from deletion.

**States.** `OPEN` (created, editable), `SIGNED` (manifest signed, immutable), `STORED`
(artifacts copied into the storing repository of package type `releasebundles`, so the
bundle survives deletion of its sources), `READY_FOR_DISTRIBUTION`. The wire names are
captured against a live Distribution before the phase closes; the internal states do not
depend on them.

**Signing.** The canonical manifest (sorted JSON of name, version, artifacts and
checksums) is signed with a detached OpenPGP signature using a signing key managed through
`/distribution/api/v1/keys/pgp` and stored in the key pair store
([0017](0017-sealed-columns-and-master-key.md) seals the private key and passphrase). The
passphrase may be supplied per request in `X-GPG-PASSPHRASE`. `keys/pgp/propagate` and the
v2 `key/propagate` push the public key to edges, where it lands in the trusted key store
(`/artifactory/api/security/keys/trusted`, already specified).

**Edges and targets.** An edge is a peer paired for the `distribution` use case. Each
distribution target records the peer, its site name, city and country code; distribution
rules (`site_name` with `*`, `city_name`, `country_codes`) select targets. An instance can
be configured with the edge role, which makes it read-only for ordinary deploys and
accepts bundle content only through distribution; any instance can also act as a source.

**Distribution.** `distribute` creates a tracker (int64 id) and one job per selected
target. A job is a peer stream that carries the signed manifest and the artifact list; the
edge verifies the signature against its trusted keys, fetches missing blobs over 0021,
deploys the artifacts to the target paths (creating missing repositories when
`auto_create_missing_repositories` is set), records the bundle as received, and reports
per-target status. A dry run answers 200 with the selected targets; a real run answers 202.
`abort` cancels queued and running jobs; `delete` removes the bundle's artifacts from the
selected edges. `dynamic/distribute` creates and distributes in one call.
Priorities order job dispatch.

**v2 distribution endpoints** (distribute, abort, remote delete, trackers, tracker
priority and the v2 signing-key endpoints under `/distribution/api/v2/distribution`) are
specified for parity and answer 501 in the Distribution envelope: they act on release
bundles v2, which belong to Lifecycle, and Lifecycle stays out of scope.

**Artifactory side.** `/artifactory/api/release/bundles` lists bundles stored as source and
received as target (`type` selects which). Bundle export writes an archive of the manifest,
signature and blobs; import reads such an archive by file name from the release bundle
import directory, configured as `distribution.import_dir` (default `<data_dir>/import`,
the same place Artifactory's release bundle import reads from), and behaves like a
received distribution. Import never reads outside that directory. Retention v1 keeps the newest N versions per bundle and versions younger
than the maximum age, honours keep marks, runs on its cron, records audit entries
(`DELETE`, `KEEP`, `UNKEEP`) and answers 405 when the service is disabled, as JFrog does.

**Permissions and tokens.** Distribution permissions (resource type `DESTINATION`) are
projections of the internal Access v2 permission's `destination` resource
([0006](0006-authorization-native-permission-model.md)). The Distribution token
endpoints issue and revoke ordinary access tokens ([0005](0005-authentication-and-tokens.md))
scoped to the distribution actions.

**Out of scope inside the API.** `trigger_xray_scan` answers 501 in the Distribution
envelope because Xray is out of scope. Distribution support bundles are produced by the
same mechanism as the system support bundle.

## Rationale

Freezing the resolved artifact list at creation and signing the canonical manifest is what
makes a bundle a trustworthy unit to promote: what is verified on the edge is exactly what
was approved at the source. Reusing the peer protocol for transfer gives distribution
ordering, retries and checksum deduplication without a second transport. Stubbing the v2
distribution endpoints rather than serving v1 bundles through them keeps the two bundle
generations from being confused on the wire.

Alternatives considered:

- *Distribute by push replication.* Replication has no manifest, no signature and no
  per-target status.
- *Signing with the instance's peer key instead of OpenPGP.* Edges and users verify
  bundles with OpenPGP keys today, and `artifactory_distribution_public_key` manages them.

## Constraints

- The release bundle create body is untyped in the oracle; the field list in research 16
  must be captured against a live Distribution before the phase closes.
- An edge must be an artifactweave instance; JFrog Edge nodes cannot receive artifactweave
  bundles.
- Bundle state wire names and the per-target status vocabulary are unverified.
- Release bundles v2 and the six v2 distribution operations answer 501 until Lifecycle is
  in scope.

## Verification

- `jf ds` create, sign and distribute against a source and an edge in `acceptance-peers`;
  the edge verifies the signature and serves the artifacts; tampering with the manifest in
  transit fails verification.
- Retention v1 tests over synthetic bundle histories, including keep marks.
- Terraform: `artifactory_distribution_public_key` and the release bundle and distribution
  webhook domains apply and fire.

## References

- [Research 16](../16-enterprise-products.md), section 1
- [0005](0005-authentication-and-tokens.md), [0006](0006-authorization-native-permission-model.md),
  [0012](0012-search-and-aql.md), [0017](0017-sealed-columns-and-master-key.md),
  [0021](0021-peer-trust-and-sync-protocol.md)
- RFC 4880, OpenPGP: <https://www.rfc-editor.org/rfc/rfc4880>
- <https://docs.jfrog.com/artifactory/reference/jfrog-distribution-api/llms.txt>
