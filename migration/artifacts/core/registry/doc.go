// Package registry runs the Docker registry's storage (decision 0031): blob
// upload sessions, manifests and tags, referrers, promotion, multi-arch
// deletes and tag retention.
//
// # Role
//
// artifacts module, core layer: the Docker and OCI registry's writes, which
// the Docker, OCI and Helm OCI repositories share. A session spans requests
// and survives a restart: it is a row in the uploads table under Backend,
// naming the blob store upload it appends to, and its repository, image and
// user, which alone may continue it. A finished upload, whether chunked,
// monolithic or mounted from another image, becomes the item
// <image>/_uploads/sha256__<hex> through the item service
// (artifacts/core/item), where a manifest push finds it. It imports the
// Docker format package for the image layout, never an ecosystem http
// package or the module's adapters (decision 0044, dependency rule 8).
//
// # Inputs
//
//   - Driving: the Docker registry protocol handlers (uploads, manifests,
//     tags, referrers, deletes), the Docker REST endpoints (promotion and
//     tag deletes), the item handlers, and the garbage collector,
//     which the composition root gives Reap for stale sessions and
//     ExpireUploads for finished uploads no manifest took in time.
//   - Driven: the metadata store (the uploads table, items and their
//     properties, through the whole store), the blob store
//     (platform/blob.Store) whose uploads the sessions append to, and the
//     item service with the format registry (Formats) and the work queue
//     (Work), so every item it writes runs its format's hooks.
//
// # Outputs
//
//   - Upload sessions (Begin, Status, Append, Finish, Cancel, Upload, Mount)
//     and the upload items they finish; Reap and ExpireUploads for the
//     collector.
//   - Manifests and tags written, promoted, renamed and removed (Push,
//     Promotion, Rename, Removal), with their parents and platform tags
//     (Parent, PlatformTag), and the referrers of a manifest.
//   - Errors: ErrUnknown, ErrSamePlace and the rest of the package's
//     sentinels, BlobsUnknownError and SizeMismatchError, which the
//     registry API answers as MANIFEST_BLOB_UNKNOWN and MANIFEST_INVALID.
//
// # Invariants
//
//   - Backend ("docker") names the sessions' rows in the uploads table, so
//     it is stored and never changes, as are the upload rows' contents and
//     the _uploads item paths.
//   - One session's requests are serialised in this process; a manifest
//     push is refused when a descriptor's size differs from the stored
//     content's, which a client would otherwise trust (decision 0031).
//   - Error texts reach clients or logs and stay byte-identical.
//
// # References
//
//   - Decision 0031: docs/research/decisions/0031-docker-oci-registry.md
//     (protocol and storage layout; line 27, upload sessions and the items
//     they finish).
//   - Research 23: docs/research/23-docker-oci-registry.md.
//   - Decision 0039: docs/research/decisions/0039-helm.md (Helm OCI through
//     the Docker registry).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 206 to 207 (core/registry), line 1034 (the mapping row) and rule
//     2 at lines 35 to 36.
//   - Plan: docs/implementation/plan.md#p6-dockeroci,
//     #p8-pypi-nuget-helm-terraform-locals and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/1011
//
// # Status
//
// Implemented P6 to P9c: the registry's uploads, manifests, tags,
// promotion and retention in P6, and Helm OCI through them in P8. Staged in
// P9d.
//
// # Testing
//
//   - registry_test.go: upload sessions across requests and restarts,
//     mounts, reaping and expiry.
//   - manifests_test.go, sizes_test.go, index_test.go, indexes_test.go and
//     platforms_test.go: manifest pushes, size checks, image indexes and
//     their platforms.
//   - promote_test.go, deletes_test.go and retention_test.go: promotion,
//     deletes and tag retention; helm_test.go: a Helm chart pushed as an
//     OCI artifact (testdata/helm, kept byte for byte by its
//     .gitattributes).
//   - format_test.go: every write through the format hooks; fault_test.go,
//     retention_fault_test.go and twins_fault_test.go: store failures.
package registry
