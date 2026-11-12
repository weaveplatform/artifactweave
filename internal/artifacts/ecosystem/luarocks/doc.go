// Package luarocks indexes root-level rockspecs and rocks and derives the
// LuaRocks manifest consumed by the native client. A rockspec or rock is
// stored at the repository root under its flat file name,
// <name>-<version>-<revision>.rockspec or
// <name>-<version>-<revision>.<arch>.rock, and its rockspec, read from the
// stored bytes (from inside the ZIP for a rock), must name the package and
// version the file name does. The package index holds a version per
// <version>-<revision>, and the work queue writes the manifest files the
// client reads, manifest and manifest-5.1 to manifest-5.5, from the stored
// files.
//
// # Role
//
// artifacts module, ecosystem layer: the LuaRocks rockspec and rock index
// rules of the LuaRocks slice. Its Handler plugs into the item service
// through the format hooks (decision 0032), so every write in a LuaRocks
// repository reaches it whichever API carried it; the slice's http package
// (artifacts/ecosystem/luarocks/http) serves the native LuaRocks protocol
// over what it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for luarocks repositories to Handler; the work
//     queue (decision 0033), which runs the manifest work (Work,
//     KindManifest) and every format's reindex through it; and the
//     protocol, which parses flat file names (Parse), checks an upstream
//     rockspec or rock before a remote caches it (ValidateContent), and
//     maps an uploaded rockspec to the numeric version id the upload client
//     sends back, and that id back to the rockspec (VersionID and
//     VersionFile).
//   - Driven: the format hooks' environment (format.Env): its blobs hold
//     the files; its store holds the items, their properties and the
//     package index (artifacts/core/pkgindex), which Reindex rebuilds from
//     an AQL search of the stored files; its work queue takes the manifest
//     work (Enqueue); and Derive writes each manifest file. The generic
//     deploy policy (artifacts/ecosystem/generic), which Handler embeds, is
//     the first check Admit applies, and a rock's ZIP is scanned within
//     bounds by platform/archive.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that check a file once before its transaction
//     (Inspect); admit only a valid rockspec or rock at a valid root file
//     name (Admit); set a stored file's properties, sync its version and
//     queue the manifest (Deployed); queue the manifest for a delete
//     (Deleting) and sync the versions a delete touched (Deleted); keep a
//     file's .asc signature with it (Sidecars); name the manifest work a
//     manifest read waits for (WorkKinds and Owed) and queue a missing
//     manifest when it is read (Resolve); class the manifest files as
//     derived (Classify); rebuild the index (Reindex); and write the
//     manifests (Work).
//   - The package index: a package per name, with a version per
//     <version>-<revision>, whose files are its rockspec and rocks.
//   - The luarocks.name, luarocks.version, luarocks.revision and
//     luarocks.arch item properties (PropName, PropVersion, PropRevision and
//     PropArch) on each stored file.
//   - The manifest files: manifest, and manifest-5.1 to manifest-5.5, each
//     filtered by the simple Lua version constraints of the matching
//     rockspec, written as Lua assignments in luarocks-admin's form.
//   - The rules the protocol shares: Identity, Parse, ValidateContent,
//     VersionID, VersionFile, ManifestPath and KindManifest.
//
// # Invariants
//
//   - Only the flat root layout the native client searches is indexed: a
//     file name is <name>-<version>-<revision> with .rockspec or
//     .<arch>.rock, and the trailing revision belongs to the version.
//   - A rockspec is at most 1 MiB with no NUL byte, and assigns package and
//     version once each, matching its file name. A rock is a ZIP of at most
//     10,000 entries and 256 MiB unpacked, holding only regular files and
//     folders at clean paths, every entry read to its end, and an embedded
//     <name>-<version>.rockspec that passes the same check.
//   - Only content that breaks those rules is refused. A stored rockspec or
//     rock that cannot be read is no fault of the file: the read's error
//     returns as it is and fails the write (500).
//   - The manifests are derived only in local and federated repositories,
//     never in a remote's cache, and hold at most 100,000 entries and
//     32 MiB. A manifest read there waits for owed work, and a read of a
//     missing manifest queues it.
//   - Names and versions are quoted in a manifest, never evaluated as Lua
//     source.
//   - The items keep the property names, so they never change; their exact
//     7.x spelling is inferred and recorded on #216.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, whose upstream files ValidateContent checks); decision
//     0008: docs/research/decisions/0008-virtual-repositories.md (virtual
//     repositories, which the protocol resolves over this index's
//     members); decision 0033:
//     docs/research/decisions/0033-persistent-work-queue.md (the manifest
//     work).
//   - docs/testing/luarocks.md (the local, federated, remote and virtual
//     qualification and the evidence), and docs/research/33-package-formats.md,
//     "LuaRocks".
//   - JFrog LuaRocks repositories:
//     https://docs.jfrog.com/artifactory/docs/luarocks-repositories, and the
//     LuaRocks manifest writer:
//     https://github.com/luarocks/luarocks/blob/main/src/luarocks/manif/writer.lua.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 234 (ecosystem/luarocks), line 379 (the slice row) and line 997
//     (the mapping row).
//   - Plan: docs/implementation/plan.md#p9b-newly-identified-artifactory-formats
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/684,
//     https://github.com/weaveplatform/artifactweave/issues/685,
//     https://github.com/weaveplatform/artifactweave/issues/686 and
//     https://github.com/weaveplatform/artifactweave/issues/1050
//
// # Status
//
// Implemented P9b (newly identified Artifactory formats). Moved here in P9d.
//
// # Testing
//
//   - luarocks_test.go: identity and rockspec admission, and a manifest the
//     native client reads as Lua assignments.
//   - edge_test.go: stored-content checks, the manifest hooks' boundaries,
//     admission and manifest size bounds, read faults, a missing package or
//     version, and a search fault that publishes no manifest.
//   - readfault_test.go: a rock whose content fails a read at any point, on
//     a store with ReadAt or one that can only seek, returns the read's
//     error, and bad rocks keep their 400 refusals.
//   - contract_test.go (external, luarocks_test): the format contract
//     (artifacts/core/formattest) and the public registry's dotted package
//     names.
package luarocks
