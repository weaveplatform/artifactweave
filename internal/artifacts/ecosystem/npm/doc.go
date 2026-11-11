// Package npm is the format of npm repositories (decision 0034). It reads
// each tarball's package.json, indexes a tarball at its canonical path as
// a version of its package (decision 0018) with the per-version Metadata
// that packuments and search are built from, and keeps the npm.*
// properties. Every write reaches it through the item service, so a
// tarball deployed by npm publish, by jf npm publish's REST deploy, or
// landed by a copy, a move or a restore is indexed the same way.
//
// A .tgz anywhere else, or one without a readable package.json, is stored
// but not indexed: every repository has strict layout (DV-041).
//
// # Role
//
// artifacts module, ecosystem layer: the format rules of the npm slice
// (package type npm). Its Handler plugs into the item service through the
// format hooks (decision 0032); the registry protocol that serves what it
// keeps is the slice's http package (artifacts/ecosystem/npm/http).
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for npm repositories to Handler; the persistent
//     work queue, which runs the strict-layout conversion (KindConvert,
//     which the server's npm operations queue) and every format's reindex
//     through Handler (decision 0033); and the registry protocol, which
//     builds packuments from the npm.* properties and each version's
//     Metadata (DecodeMetadata) by the latest rules (Settings, Candidate),
//     and checks a publish with ReadStored, ValidName and CanonicalPath.
//   - Driven: the format hooks' environment (format.Env), through which
//     Handler reads stored tarballs, keeps the package index
//     (artifacts/core/pkgindex), finds tarballs by AQL (artifacts/core/aql)
//     and moves them to their canonical paths; and the generic deploy
//     policy (artifacts/ecosystem/generic), which Handler embeds.
//   - Settings: the server's npm options (decision 0034), which reach
//     Handler through ecosystem.Settings.
//
// # Outputs
//
//   - Handler: the hooks that inspect and admit a write (Admit adds the
//     release and snapshot handling and npm.enforce_path to the generic
//     policy), index a tarball, follow its dist-tags and deprecation
//     (Annotated), settle latest after a delete, and rebuild the index or
//     convert a repository to strict layout as queued work.
//   - The npm.* properties (PropName to PropDeprecated) and each version's
//     Metadata in the package index.
//   - The rules the registry protocol shares: Tarball, ReadTarball and
//     ReadStored; CanonicalPath, CleanVersion, ValidName and Keywords; and
//     Settings, Candidate and Outranks, which decide latest.
//
// # Invariants
//
//   - A dist-tag names one version of its package, latest included, and
//     latest moves only by decision 0034's rules, whichever route changed
//     the tarballs.
//   - Only a tarball at its canonical path is indexed; KindConvert moves
//     the others there, as 7.x's background conversion does.
//   - A read waits for no queued work (Owed is empty): the deploys keep
//     the index current, as 7.x's reads assume.
//   - Statuses and messages a client sees stay byte-identical.
//
// # References
//
//   - Decision 0034: docs/research/decisions/0034-npm.md ("Storage and
//     properties", "Dist-tags and latest", "Strict layout, conversion and
//     reindex").
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md
//     (the conversion and reindex as queued work).
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0018: docs/research/decisions/0018-package-index.md.
//   - docs/operations/npm-remote.md (the remote cache this format indexes).
//   - JFrog npm repositories, as decision 0034 cites it:
//     https://docs.jfrog.com/artifactory/docs/npm-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 227 (ecosystem/npm), "npm ecosystem" (lines 413 to 445), line
//     664 (virtual.go) and line 997 (the mapping row).
//   - Plan: docs/implementation/plan.md#p7-npm-go-maven-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/1031
//
// # Status
//
// Implemented P7 (npm local repositories), with remote repositories in P9.
// Staged in P9d. Roadmap: virtual.go (P10), the Merger that merges
// packuments for virtual repositories.
//
// # Testing
//
//   - handler_test.go, latest_test.go and overwrite_test.go: indexing,
//     properties, dist-tags, deprecation and latest across deploys,
//     overwrites, transfers and reindexes.
//   - tarball_test.go and enforce_test.go: reading a tarball, names,
//     versions and canonical paths, and npm.enforce_path.
//   - work_test.go and fault_test.go: the queued reindex and conversion,
//     store faults and corrupt metadata.
//   - contract_test.go: the format contract suite (artifacts/core/formattest).
package npm
