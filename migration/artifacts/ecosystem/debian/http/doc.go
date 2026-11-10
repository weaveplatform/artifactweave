// Package debianhttp serves the Debian protocol over the item kit: the
// Packages and Sources indexes, handed only to a caller who may read every
// package they list; a virtual repository's apt metadata, composed from its
// members and signed with its key pair when it names one, and the packages
// that metadata lists; the artifactory spec's metadata calculation (POST
// /api/deb/reindex/{repoKey}); and snapshots, which copy a distribution's
// metadata into a local repository (POST /api/deb/{repoKey}/snapshot), run
// as queued work, and are read under /api/deb/{repoKey}/snapshots/{tag}/.
//
// # Role
//
// artifacts module, ecosystem debian, HTTP layer: the Debian slice's
// protocol handlers and its snapshot work, beside the format rules of
// artifacts/ecosystem/debian. It is named debianhttp so that it does not
// shadow net/http. Outside tests, only its own http subtree, the module's
// http adapters and app may import it (decision 0044, dependency rule 9);
// app does so to register the snapshot work, and the server root counts as
// app until it moves there.
//
// # Inputs
//
//   - Driving: the router, which embeds DebianAPI in the artifactory spec's
//     strict server, lists TryDebianItem seventh among the item kit's
//     overlays (itemkit.Deps.Overlays), so that an item GET or HEAD in a
//     Debian repository reaches it before the generic lookup, and mounts
//     the snapshot reads with RegisterDebianSnapshotRoutes among the item
//     routes; app, which registers the snapshot work with
//     RegisterDebianSnapshotWork before the work queue runs; and the work
//     queue, which runs each queued snapshot.
//   - Driven: the item kit (artifacts/adapters/http/itemkit), which resolves
//     the request and the caller's access, reads items and blobs, serves
//     stored files and remote streams, deploys a snapshot's metadata through
//     the item service, runs the reindex, queues and awaits work, and writes
//     errors; the kit's remote source service, which fetches a remote's or a
//     remote member's metadata and packages under the kit's Debian source
//     request (itemkit.DebianSourceRequest); access/api, whose Authenticator
//     and Authorizer recheck a queued snapshot's caller and whose Keys read a
//     virtual repository's GPG key pair; the work queue (platform/work); the
//     generic download policy (artifacts/ecosystem/generic); and the format
//     package (artifacts/ecosystem/debian), which decodes and checks indexes
//     and Release files, composes and signs the virtual view, and marks a
//     snapshot's writes (WithSnapshotWrite).
//
// # Outputs
//
//   - DebianAPI: the Debian partial strict server over the kit.
//     CalculateDebianRepositoryMetadata answers the metadata calculation,
//     queueing its writeProps=0 form (debian.KindReindexNoProps) itself, and
//     CreateDebianSnapshot answers the snapshot creation: queued and, unless
//     async=1, awaited, or run at once when the kit has no work queue.
//   - TryDebianItem: the overlay that serves the Packages and Sources
//     indexes and their by-hash copies, a virtual repository's metadata under
//     dists/ and its binary packages, and leaves every other request to the
//     overlays after it and the generic lookup.
//   - RegisterDebianSnapshotRoutes: mounts GET and HEAD of
//     /artifactory/api/deb/{repoKey}/snapshots/{tag}/*, which serve a
//     snapshot's metadata from the target repository and redirect a package
//     path to the recorded source repository.
//   - RegisterDebianSnapshotWork: registers the snapshot handler on the work
//     queue.
//
// # Invariants
//
//   - The snapshot work kind is "debian-snapshot" and a job's key is
//     <target>/<tag>. Both are stored with the queued work, so they stay
//     byte-identical: a job queued before a change must still find its
//     handler.
//   - RegisterDebianSnapshotWork runs before the queue does: only registered
//     kinds are leased, and an item of a kind with no handler waits
//     (decision 0033), so a missing registration fails nothing and leaves
//     every snapshot queued.
//   - Every attempt of a queued snapshot, a retry after a restart included,
//     rechecks the caller's credential reference (Authenticator.Current) and
//     evaluates the caller's permissions as they are now
//     (Authorizer.Authorize). An invalid job, an expired caller or a kit
//     without an authenticator refuses the job for good (work.ErrPermanent).
//   - A snapshot copies metadata only, into snapshots/<tag>/ of a local
//     Debian target: the source distribution's Release, each index it
//     declares, bound to the size and SHA-256 the Release gives (at most 64
//     MiB each and 512 MiB in all), and InRelease and Release.gpg where they
//     exist. A read of a package path redirects to the recorded source. A
//     request first claims the tag's folder, so a request from another
//     source or distribution answers 409, and a retry of the same request
//     completes it.
//   - A Packages or Sources index, stored, cached or in a snapshot, reaches
//     only a caller who may read every package it lists, which the generic
//     item route cannot check.
//   - A virtual repository's metadata is composed for each request from its
//     members in order, at most 128. A member of another type, a virtual
//     member or one with the trivial layout is refused, and a package path
//     belongs to the first member that lists it. A member's index must match
//     the size and SHA-256 its Release gives, and only the packages the
//     caller may read are listed. A key pair, GPG only, signs the Release as
//     InRelease and Release.gpg.
//   - Statuses and error and log text stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format routes come before the generic item lookup, in a fixed order);
//     decision 0033: docs/research/decisions/0033-persistent-work-queue.md
//     (the snapshot runs as queued work, and a kind with no handler waits).
//   - docs/testing/debian.md (connecting apt, virtual repositories, the
//     metadata calculation, snapshots and signing).
//   - JFrog Debian repositories:
//     https://docs.jfrog.com/artifactory/docs/debian-repositories; the
//     metadata calculation API:
//     https://docs.jfrog.com/artifactory/reference/calculatedebianrepositorymetadata;
//     and the snapshot API:
//     https://docs.jfrog.com/artifactory/reference/createdebiansnapshot.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 45 (the ecosystem http rule) and line 407 (the root's Debian
//     files).
//   - Plan: docs/implementation/plan.md#p29-linux-package-repositories
//     and #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/758 and
//     https://github.com/weaveplatform/artifactweave/issues/1083
//
// # Status
//
// Implemented P9c, which delivered the plan's P29 group (issues #757 to
// #760), as the server root's debian.go, debian_operations.go,
// debian_snapshot.go and debian_virtual.go. P9d made it the Debian partial
// strict server with its own snapshot route registrar (WEB-08), and moved
// it here, with the snapshot work taking the item kit (WEB-14).
//
// # Testing
//
// The tests call the protocol's code without the router, over in-memory
// stores (fixture_test.go, which keeps the names of the root fixtures the
// tests used):
//
//   - debian_snapshot_test.go: a snapshot claim bound to its source and
//     distribution, the repositories and permissions a snapshot needs,
//     completed and claimed tags kept, and the queued work under the
//     caller's current permissions and without an authenticator.
//   - debian_snapshot_security_test.go: snapshot metadata that is missing,
//     oversized, misdigested or malformed is refused, and a claimed tag
//     cannot pass to another source; its snapshot request runs through the
//     artifactory strict handler with the Debian partial alone.
//   - debian_source_test.go: the kit's Debian source request checks a
//     remote's packages and metadata.
//   - debian_index_test.go: Packages and Sources indexes refused with a
//     query, in a blacked-out repository or to a caller who may not read
//     them; repositories that have no index; local index content that is a
//     folder, unreadable or malformed; and remote indexes streamed, cached,
//     over the size bound, or listing a package the source refuses or
//     cannot check.
//   - debian_virtual_test.go: virtual metadata refused, merged from the
//     readable members, and failing closed on store faults, wrong member
//     types and unusable signing keys; a remote member's Release and
//     Packages through its source; and virtual packages served from the
//     first member that holds them.
//   - debian_operations_test.go: the rebuild that writes no properties, for
//     an absent repository, an unreadable package and a queue that refuses
//     it.
//   - debian_snapshot_fault_test.go: snapshot requests and reads without a
//     store or a caller, and requests whose caller's permissions cannot be
//     read; queued snapshots over the work key bound or refused by the
//     queue or their run; the work handler registered once and refusing
//     jobs it cannot run; and claims and snapshots failing closed on store,
//     blob and permission faults, Release declarations that do not parse,
//     excluded packages, an unreadable InRelease and a package a remote
//     source refuses.
//
// debian_index_test.go and debian_virtual_test.go serve their item GETs
// through the kit with TryDebianItem as the only overlay
// (overlay_fixture_test.go).
//
// The route suite test/server/artifacts/debian checks the snapshot reads'
// guards through the router, and the @debian acceptance partition
// (test/acceptance/features/debian.feature) is the native apt evidence.
package debianhttp
