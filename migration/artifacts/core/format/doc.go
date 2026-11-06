// Package format is the contract between the item service and the package
// formats (decision 0032): the hooks every format implements, the optional
// interfaces a format adds for what it needs, the environment they run in,
// and the errors with which a format refuses a request or takes a write
// without making it. The item service calls the hooks on every write,
// whichever API carried it, so a format's package index (decision 0018) and
// derived items stay consistent with the item tree, which remains the source
// of truth.
//
// # Role
//
// artifacts module, core layer: the format contract. Each package type's
// format is a package of its own, named after its ecosystem
// (docs/implementation/repo-layout.md), and internal/format/formats picks
// one for a package type. Formats never import the HTTP layer, so they speak
// to clients only through RejectionError and AcceptedError (decision 0032).
// As artifacts core, the contract never imports the module's adapters or an
// ecosystem http package (decision 0044, dependency rule 8).
//
// # Inputs
//
//   - Driving: the item service (internal/service/item) calls the hooks and
//     the optional interfaces on every deploy, checksum upload, folder
//     create, copy, move, delete, restore, property change and download,
//     with the Env it builds for the operation. The build and registry
//     services take a Handler for each package type, remote retrieval
//     classifies items with ClassOf, and the reindex API and configuration
//     apply queue the reindex kinds with a WorkRequest.
//   - Driven: the format packages implement Handler and the optional
//     interfaces they need: Inspector, Admitter, TransferSourceGuard,
//     WriteGuard, FolderAdmitter, AnnotationAdmitter, Resolver, Annotator,
//     Reindexer, PathReindexer, Worker, Classifier, SidecarOwner and
//     SidecarKeeper. The persistent work queue runs a Worker's items.
//   - Env: the metadata store, the read half of the blob store (Blobs), the
//     clock, the repository layouts (artifacts/core/layout), and the item
//     service's Derive, Underive, Remove, Move and Enqueue, with the
//     inspection of the file being written and the reindex options
//     (ReindexSource, CheckReindex and SkipProperties).
//
// # Outputs
//
//   - Hook errors. An error from a hook inside a write's transaction rolls
//     the write back.
//   - *RejectionError, made by Rejectf: a format's refusal. The HTTP layer
//     answers it with Status and Message in the envelope of the surface the
//     request arrived on; a copy or move reports it and skips the item.
//   - *AcceptedError: a deploy or checksum upload that Admit takes without
//     storing it. The HTTP layer answers Status with an empty body, and an
//     exploded archive leaves the entry out.
//   - What Admit and Resolve decide: the Write as it should happen, and the
//     path a download reads instead of the one requested.
//   - The work kinds of the JFrog reindex operations (KindReindex,
//     KindReindexNoProps, KindReindexPath) and their payload, WorkRequest;
//     WorkRef names the work a download waits for.
//   - Class and ClassOf: remote cache expiry reads them, and replication
//     (P11) and federation (P13) will.
//   - MinUniqueSnapshots and UniqueSnapshots: how many unique builds a
//     maxUniqueSnapshots cleanup keeps.
//
// # Invariants
//
//   - The required hooks run inside the transaction that changes the item,
//     so an error rolls the change back. Inspect runs before the write's
//     transaction, so no parsing holds the database's write lock; Work runs
//     outside any transaction, and a Reindexer manages its own.
//   - Derive and Underive call no hook, so a format's own output never
//     re-enters it.
//   - The HTTP layer passes a RejectionError's Status and Message to the
//     client, so message texts are part of each format's API and stay
//     byte-identical.
//   - The package is named format and Rejectf keeps its name: the wrapcheck
//     extra-ignore-sigs entry "format.Rejectf(" in .golangci.yml matches by
//     name, so a rename would fail lint at every return of a rejection.
//   - Work kinds are code constants, unique across formats, and Work is
//     idempotent, since an item can run again.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md,
//     lines 28 to 49 (the hooks and the hook environment) and 71 to 73
//     (RejectionError, and formats never import the HTTP layer), with its
//     amendments on source transfer restrictions, writes taken without
//     being made and sidecars a format keeps.
//   - Decision 0018: docs/research/decisions/0018-package-index.md.
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md
//     (Enqueue, Worker and the reindex kinds).
//   - Decision 0034: docs/research/decisions/0034-npm.md (Move, for the
//     strict-layout conversion; npm ignores maxUniqueSnapshots).
//   - Decision 0039: docs/research/decisions/0039-helm.md (SidecarKeeper).
//   - Decision 0041: docs/research/decisions/0041-terraform-backend.md
//     (AnnotationAdmitter).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Artifacts and remote" (the injected registry) and "Dependency rules".
//   - Research 24: docs/research/24-package-format-handlers.md.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 195 to 197 (core/format), line 996 (the contract) and rule 2 at
//     lines 35 to 36.
//   - The floor of two unique snapshots:
//     https://jfrog.com/help/r/artifactory-how-does-the-max-unique-snapshots-parameter-work-in-artifactory/how-does-this-feature-work
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape,
//     #p6-dockeroci, #p7-npm-go-maven-locals,
//     #p8-pypi-nuget-helm-terraform-locals, #p9-remote-repositories, the P9c
//     row of #build-status (https://github.com/weaveplatform/artifactweave/issues/753)
//     and #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/969
//
// # Status
//
// Implemented P3 to P9c. P3 added Handler with Deployed and Deleting, and P6
// added Deleted for Docker's tag index. P7, format framework v2, added Env,
// the Inspector, Admitter, Resolver, Annotator, Reindexer and Classifier
// interfaces with the item classes, RejectionError, SidecarOwner and
// AcceptedError for Maven, Worker and WorkRef, and the snapshot limit. P8
// added SidecarKeeper, PathReindexer, the reindex kinds and WorkRequest,
// FolderAdmitter, AnnotationAdmitter and WriteGuard; P9 the reindex source
// and caller checks for cached remote packages; P9c TransferSourceGuard,
// Opkg's reindex that leaves item properties alone (writeProps=0) and
// Write.By.
// Staged in P9d. Roadmap: the injected Registry type
// (https://github.com/weaveplatform/artifactweave/issues/970), Env's lease
// and work-queue stores
// (https://github.com/weaveplatform/artifactweave/issues/1029) and a narrow
// format.Store in place of the whole store
// (https://github.com/weaveplatform/artifactweave/issues/1087).
//
// # Testing
//
//   - TestUnitClassOf: a Classifier's own class, and ClassUnique for a
//     format without one.
//   - TestUnitRejectf and TestUnitAcceptedError: the error texts.
//   - TestUnitUniqueSnapshots: no limit, or one of 0 or less, keeps every
//     build, and any other limit keeps at least MinUniqueSnapshots.
//   - formattest.Run (internal/format/formattest) is the contract suite: the
//     format packages' tests run it through the item service on each store
//     backend.
package format
