# 0024: Cold storage tier

Checked against: Artifactory 7.161.32

Status: Proposed

## Context

JFrog archives rarely used content to a separate cold Artifactory instance (self-hosted,
Enterprise X and Enterprise+) or through smart archiving policies to an archive instance in
JFrog Cloud (Enterprise+; SaaS from 7.111, a self-hosted active instance from 7.117.14);
both keep the content searchable and restorable from the live instance
([research 16](../16-enterprise-products.md#2-cold-storage-archive-and-retention),
[archive](https://docs.jfrog.com/administration/docs/archive.md)). The
owner brought both API families into scope on 2026-10-13 and chose a cold blob tier inside
the same instance over a second cold instance.

## Decision

**Two blob stores.** The application configuration may define a `cold_blob` store next to
the hot `blob` store ([0004](0004-storage-sql-metadata-and-blobs.md)), using the same
backends: local disk, S3, Google Cloud Storage, Azure Blob Storage or a mirror of them
([0027](0027-cloud-blob-backends.md), [0028](0028-blob-mirroring.md)), typically a bucket
or container whose default storage class is an archive or infrequent-access class. Without a cold store configured, archive policies can be created but runs fail with
a clear error and the namespace endpoint still answers.

**Archiving moves items, not just bytes.** A run selects items or package versions, then
for each item:

1. copies the blob to the cold store and verifies its sha256;
2. writes an archived-item record holding the item's full metadata (repository, path,
   name, checksums, size, properties, statistics, creation and modification data, the
   package and version when archived as a package), the policy key, the run id and the
   archive time;
3. removes the item from the hot index (through the trash can unless `skipTrashcan`), and
   lets blob garbage collection reclaim the hot copy once nothing references it.

A package-level archive moves every file of the version together and removes the version
from the package index ([0018](0018-package-index.md)).

**Two policy families, one engine.** Cold-storage archive policies
(`/api/retention/archive/policies`, selection by repositories, paths, names, properties,
age or a custom AQL query) and smart archiving policies (`/api/archive/v2/packages/policies`,
the cleanup-policy envelope) are stored separately with their own wire shapes and
statuses, and run by the same archiving engine and scheduler
([0010](0010-retention-trash-and-gc.md)). `preview` and `convertToAql` evaluate the
selection without moving anything. Runs take a lease so one policy never overlaps itself,
honour `durationInMinutes`, and report the execution status fields JFrog documents.

**Search.** `/api/retention/search` runs AQL over the archived-item records with the
items domain only; `searchPackages` searches archived package versions. Results use the
AQL envelope.

**Restore.** `/api/retention/restore` and `/api/archive/v2/restore/packages` create a
restore job that copies each blob back from the cold store, recreates the item at its
original path (or the fallback, central or new destination when the original is taken or
not requested), restores its properties and re-indexes packages. Restore is
non-destructive: the archived record and the cold blob stay until expiry. A job is capped
at 30,000 artifacts by default, as in JFrog
(`artifactory.retention.warm.restore.artifact.limit`, confirmed for 7.x by
[cold storage](https://docs.jfrog.com/administration/docs/cold-storage.md)).

**Expiry.** `expirationTimeInMonths` purges archived records and their cold blobs once
they reach the age; purging is a run with its own status.

**No cold instance to pair.** The `artifactory-cold` pairing use case on the
`service_trust/pairing` endpoints answers 404, since there is no separate cold instance to
bind.

**Namespace.** Each instance generates a stable namespace id at first start and returns it
from `/api/retention/namespace`; archived records carry it so content restored into
another instance can be told apart.

## Rationale

A cold tier inside the instance delivers the cost saving that motivates cold storage
without operating a second registry, and the object stores' archive and infrequent-access
storage classes already give the cheaper medium. Keeping full metadata in the hot database while the bytes move means search,
preview and restore never touch the slow tier until content is actually needed. Running
both JFrog policy families on one engine avoids two archiving implementations for one
purpose.

Alternatives considered:

- *A separate cold artifactweave instance (JFrog's model).* Recorded as a possible later
  option; it would reuse the peer protocol ([0021](0021-peer-trust-and-sync-protocol.md))
  as the transport.
- *Changing the storage class of objects in place.* Ties archiving to each provider's
  storage-class API and makes local disk deployments unable to archive.

## Constraints

- Restoring from archive storage classes can take hours on some providers; the restore
  job reports progress and does not block requests.
- Archived records live in the hot database, which therefore grows with archived content;
  the records are compact and indexed for search only.
- The JFrog namespace field is named `jfrogColdStorageNamespaceId` on the wire and is
  kept as named.
- Two 7.x behaviours this decision does not settle yet; P16 settles them:
  - A package version already in cold storage is not archived again: Artifactory keeps
    the older archived copy and skips the newer one with an info log (7.71.2,
    [legacy notes](https://releases.jfrog.io/artifactory/legacydocs/Artifactory-Self-Managed-Release-Notes-End-of-Life.pdf)).
  - Archiving from a federated repository is not synced to the other members, a limitation
    JFrog still documents ([cold storage](https://docs.jfrog.com/administration/docs/cold-storage.md)).

## Verification

- Acceptance with MinIO as the cold tier: a policy archives eligible versions, search finds
  them, restore returns byte-identical content with properties, and expiry purges them.
- Terraform: `artifactory_archive_policy` applies, plans clean and destroys.
- Engine tests for preview, AQL conversion, lease exclusivity and partial failures.

## References

- [Research 16](../16-enterprise-products.md), section 2
- [0004](0004-storage-sql-metadata-and-blobs.md), [0010](0010-retention-trash-and-gc.md),
  [0012](0012-search-and-aql.md), [0018](0018-package-index.md)
- <https://docs.jfrog.com/administration/docs/cold-storage.md>
- <https://docs.jfrog.com/administration/docs/archive.md>
