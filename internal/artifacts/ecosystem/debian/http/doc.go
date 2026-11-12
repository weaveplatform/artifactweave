// Package debianhttp serves the Debian protocol over the item kit: the
// Packages and Sources indexes, under the download policy at their own path
// and handed only to a caller with Read permission on every package they
// list; a virtual repository's apt metadata, composed from its
// members and signed with its key pair when it names one, the packages
// that metadata lists, and every other file under dists/ from the first
// member that holds it; the artifactory spec's metadata calculation (POST
// /api/deb/reindex/{repoKey}); and snapshots, which copy a distribution's
// metadata into a local repository (POST /api/deb/{repoKey}/snapshot), run
// as queued work, and are read under /api/deb/{repoKey}/snapshots/{tag}/.
//
// # Role
//
// artifacts module, ecosystem debian, HTTP layer: the Debian slice's protocol
// handlers and its snapshot work, beside the format rules of
// artifacts/ecosystem/debian. It is named debianhttp so that it does not
// shadow net/http. Outside tests, only its own http subtree, the module's http
// adapters and app may import it (decision 0044, dependency rule 9); app does
// so to mount it and to register the snapshot work.
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
//   - Driven: the item kit (artifacts/adapters/http/itemkit), whose store
//     (itemkit.Store) the overlay is handed and the handlers read
//     repositories and items from, claiming a snapshot in one of its
//     transactions, and which resolves the request and the caller's access,
//     reads items and blobs, serves stored files and remote streams, deploys
//     a snapshot's metadata through the item service, runs the reindex,
//     queues and awaits work, and writes errors; the kit's remote source
//     service, which fetches a remote's or a remote member's metadata and
//     packages under the kit's Debian source request
//     (itemkit.DebianSourceRequest); access/api, whose Authenticator and
//     Authorizer recheck a queued snapshot's caller and whose Keys read a
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
//     indexes and their by-hash copies, a virtual repository's files under
//     dists/, composed or from a member, and its binary packages, and leaves
//     every other request to the overlays after it and the generic lookup.
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
//     exist. A read of the snapshot's metadata is a read of the target's
//     stored file, under the target's download policy; a read of a package
//     path redirects to the recorded source, whose policy answers the
//     redirected request. A request first claims the tag's folder, so a
//     request from another source or distribution answers 409, and a retry
//     of the same request completes it. The source's Release, each index it
//     declares, InRelease and Release.gpg are read under the source's
//     download policy at their own path, after the Read permission check
//     (DV-057): a blacked-out source, or patterns that reject one of them,
//     answer the policy's 404 and message naming <source>:<path>, as a GET
//     of that file would, and the target gets no snapshot. A caller who may
//     read the Release, which the request checks first, but not a file the
//     snapshot then reads, or a package an index lists, is refused as that
//     first check refuses: 403, or 401 to an anonymous caller.
//   - A Packages or Sources index, stored, cached, in a snapshot or passed
//     through a virtual repository, reaches only a caller with Read
//     permission on every package it lists, which the generic item route
//     cannot check. A Debian installer's Packages index, which a remote
//     repository relays as metadata, is read with the permission on its
//     own path, as a translation or a Contents index is [I]. The download
//     policy does not filter or refuse an index for the packages it lists:
//     a package whose file the include and exclude patterns reject stays
//     listed and only its download is refused, as in Artifactory's indexes
//     (RTFACT-30830, RTFACT-30656).
//   - An index, a virtual repository's metadata and a virtual package are
//     read under the download policy at their own path, after the Read
//     permission check (DV-057): a blacked-out repository, or patterns that
//     reject the path, answer the item route's 404 and message, and a
//     virtual repository's own patterns answer that no repository serves
//     the request. A virtual repository's GET of a package, or of a dists/
//     file it does not compose, passes over a member that refuses it to the
//     next member.
//   - A virtual repository composes only its Release, InRelease and
//     Release.gpg, its Packages indexes and their by-hash copies, each
//     index plain, gzipped and in each of the virtual repository's own
//     optionalIndexCompressionFormats (bz2, lzma, xz). Any other
//     file under dists/, such as a component Release, a Contents index, a
//     translation, an installer file or a Sources index, comes from the
//     first member, in order, that may serve it and holds it (RTFACT-19086,
//     RTFACT-30014); the order is the configured one until #1252 settles
//     it. A remote member's upstream is asked for any such file; the member
//     lacks only a name its Debian source request refuses, such as a
//     by-hash name that is no SHA-256, as its own GET answers.
//   - An index, a virtual repository's metadata, a virtual package and the
//     other dists/ files take a query and matrix parameters as any other
//     file's GET does, once read permission is settled: a query, such as
//     skipUpdateStats (RTFACT-25636), changes nothing, and matrix parameters
//     are matched against the file's properties (JFrog's guide, Using
//     Properties in Deployment and Resolution). A virtual repository's
//     composed metadata carries none [I], so only a mandatory key refuses
//     it, and a member whose file does not match is passed over for the
//     next [I]. A remote repository's index still answers 404 to a query or
//     a matrix parameter, after the read check, since whether a remote
//     passes them on to its upstream is unsettled; a virtual repository
//     passes over a remote member for its Sources index so.
//   - A virtual repository's metadata is composed for each request from its
//     members in order, at most 128. A member of another type, a virtual
//     member or one with the trivial layout is refused, a member that
//     refuses its Release or index (blacked out, for one) is left out, and a
//     package path belongs to the first member that lists it. A member's
//     index must match the size and SHA-256 its Release gives, and only the
//     packages the caller has Read permission on are listed. A key pair, GPG
//     only, signs the Release as InRelease and Release.gpg.
//   - A server built without a remote source service answers a remote
//     index read, and a virtual repository's metadata or package read that
//     reaches a remote member, 500, a fault of its own, not the upstream's.
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
//   - https://github.com/weaveplatform/artifactweave/issues/758,
//     https://github.com/weaveplatform/artifactweave/issues/1083,
//     https://github.com/weaveplatform/artifactweave/issues/1090,
//     https://github.com/weaveplatform/artifactweave/issues/1254 and
//     https://github.com/weaveplatform/artifactweave/issues/1258
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
//     caller's current permissions, without an authenticator, and answering
//     the source's download policy.
//   - debian_snapshot_security_test.go: snapshot metadata that is missing,
//     oversized, misdigested or malformed is refused, and a claimed tag
//     cannot pass to another source; its snapshot request runs through the
//     artifactory strict handler with the Debian partial alone.
//   - debian_source_test.go: the kit's Debian source request checks a
//     remote's packages and metadata.
//   - debian_source_request_test.go: the source request names what apt
//     reads from an upstream (#1273): an installer's Packages index, any
//     by-hash file, a .lzma index and a package under a by-hash folder,
//     each checked as it streams, with the 64 MiB bound on an index's
//     by-hash copy alone; names it cannot check stay not found.
//   - debian_upstream_paths_test.go: a remote repository asks its upstream
//     for those paths and a virtual repository for its remote member's
//     installer index; a virtual repository composes the forms its own
//     optionalIndexCompressionFormats names (#1274); a local .lzma index is
//     read as an index; and a snapshot of a remote copies the installer and
//     .lzma indexes its Release lists.
//   - debian_index_test.go: Packages and Sources indexes refused to a
//     caller who may not read them or a package they list, and
//     with the download policy's 404 in a blacked-out repository, local or
//     remote, or one whose patterns reject the index; a package the patterns
//     reject kept listed while its GET is refused; repositories that have no
//     index; local index content that is a folder, unreadable or malformed;
//     and remote indexes streamed, cached, over the size bound, or listing a
//     package the source refuses or cannot check.
//   - debian_virtual_test.go: virtual metadata refused, including by the
//     virtual repository's own patterns, merged from the readable members,
//     and failing closed on store faults, wrong member types and unusable
//     signing keys; packages a local or remote member's patterns, or the
//     virtual repository's own, reject kept listed while their GET passes
//     over the refusing member; a remote member's Release and Packages
//     through its source; virtual packages served from the first member
//     that holds them; and remote index and virtual package reads on a
//     server built without remote sources.
//   - debian_virtual_files_test.go: the dists/ files a virtual repository
//     does not compose, each kind from a local or a remote member and from
//     the first member that serves it, past a remote member whose upstream
//     lacks it; refused to a caller who may not read
//     them, by the virtual repository's patterns and by an upstream fault,
//     not found where no member holds them; and a member's
//     Sources index refused to a caller who may not read a file it lists,
//     and failing closed when it cannot be read, parsed or checked.
//   - debian_index_query_test.go: matrix parameters on a stored index, a
//     virtual package and a virtual Sources index failing closed when the
//     file's properties cannot be read; a virtual repository passing over a
//     member, a remote member's stream or cached file included, whose file
//     they do not match; and its Sources index passing over a remote member
//     for a query or a matrix parameter without asking its upstream.
//   - debian_operations_test.go: the rebuild that writes no properties, for
//     an absent repository, an unreadable package and a queue that refuses
//     it.
//   - debian_snapshot_fault_test.go: snapshot requests and reads without a
//     store or a caller, and requests whose caller's permissions cannot be
//     read; queued snapshots over the work key bound or refused by the
//     queue or their run; the work handler registered once and refusing
//     jobs it cannot run; claims and snapshots failing closed on store,
//     blob and permission faults, Release declarations that do not parse,
//     a listed package the caller may not read, an unreadable InRelease and
//     a package a remote source refuses; snapshots of a local or remote
//     source whose patterns reject a listed package keeping it listed, the
//     snapshot's index still served once the source is blacked out; a
//     snapshot's metadata refused by a blacked-out target or its patterns;
//     and a snapshot whose source is blacked out, or whose patterns reject
//     its Release, an index or its InRelease, answering the policy's 404 and
//     message, while a caller who may not read an index is refused 403.
//   - debian_snapshot_permission_test.go: a real permission target that
//     lets the caller read the source's Release but not the indexes it
//     declares; the snapshot answers 403, or 401 to an anonymous caller, at
//     once and through the work queue, also where patterns reject the
//     index, and the target gets no snapshot.
//
// debian_index_test.go, debian_index_query_test.go, debian_virtual_test.go
// and debian_virtual_files_test.go serve their item GETs through the kit
// with TryDebianItem as the only overlay (overlay_fixture_test.go).
//
// The route suite test/server/artifacts/debian checks the snapshot reads'
// guards and a remote's relay of upstream files through the router, and
// the @debian acceptance partition
// (test/acceptance/features/debian.feature) is the native apt evidence.
package debianhttp
