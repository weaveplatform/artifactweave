// Package alpine implements Alpine APK v2 package and index metadata. It
// checks every package a repository stores or caches, keeps the package
// index and the alpine.* item properties in step with the packages, and
// derives the signed APKINDEX.tar.gz of each architecture folder of a
// local or federated repository.
//
// # Role
//
// artifacts module, ecosystem layer: the Alpine APK v2 package, index and
// signing rules of the Alpine slice. Its Handler plugs into the item
// service through the format hooks (decision 0032), so every write and
// delete in an Alpine repository reaches it whichever API carried it, and
// it rebuilds the indexes as queued work (decision 0033). The Alpine
// protocol is the http subpackage (alpinehttp), which builds a virtual
// repository's signed index and selects its packages with this package.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for alpine repositories to Handler; the work
//     queue, which runs the index rebuild (KindIndex) through Handler.Work
//     and every format's reindex, the reindex operation's included,
//     through Handler.Reindex; the protocol (alpinehttp), which reads
//     member indexes (DecodeIndex) and writes a virtual repository's
//     (EncodeIndex, SigningKey); and the item kit
//     (artifacts/adapters/http/itemkit), whose remote source request checks
//     an upstream index or package before a remote caches it
//     (ParseIndexPath, DecodeIndex, ParsePath, ReadPackage), and whose item
//     GET sends a remote's downloads through the remote source (Serves).
//   - Driven: the format hooks' environment (format.Env): its blobs hold
//     the packages; its store holds the items, their properties and the
//     package index (artifacts/core/pkgindex), and lists a repository's
//     files by AQL (artifacts/core/aql); Enqueue asks for the index
//     rebuild when the write commits; Derive and Underive write and remove
//     the indexes; and Keys (access/api) reads the RSA signing pair, whose
//     private key platform/keymaterial decodes. The generic deploy policy
//     (artifacts/ecosystem/generic) is the first check Admit applies.
//   - Settings: none. A local or federated repository's primaryKeyPairRef,
//     or a virtual repository's keyPair, names its signing pair; a
//     repository that names none publishes unsigned indexes.
//
// # Outputs
//
//   - Handler: the hooks that read a package before its transaction
//     (Inspect), admit only a valid package at a valid path and refuse a
//     write of an index to a repository that derives its own (Admit), keep
//     the package index and properties in step with deploys and deletes
//     (Deployed, Deleting, Deleted), class the indexes as derived
//     (Classify), make an index read wait for its rebuild (Owed), derive
//     the indexes (WorkKinds, Work) and rebuild the package index
//     (Reindex).
//   - The package rules: Package, ParsePath and ReadPackage.
//   - The index rules: ParseIndexPath, DecodeIndex, EncodeIndex,
//     Package.IndexRecord and SigningKey.
//   - The stored names: KindIndex, IndexName and the Prop* property names.
//
// # Invariants
//
//   - A package's path is
//     <branch>/<repository>/<architecture>/<name>-<version>.apk, and its
//     .PKGINFO name, version and architecture must match it: a file name
//     alone never gives a package its identity.
//   - A package's data stream must hash to the datahash its .PKGINFO
//     declares, give every file the SHA-1 apk checks and every entry its
//     parent folder, and stay within fixed bounds (8 MiB of control
//     prefix, 200,000 data entries, 4 GiB expanded).
//   - An index's C: field is the SHA-1 of the package's compressed control
//     stream, the value apk-tools binds a package with, never a digest of
//     the whole archive.
//   - A signed index starts with a .SIGN.RSA.<key name> stream that holds
//     an RSA PKCS #1 v1.5 signature over the SHA-1 of the compressed index,
//     as abuild-sign writes it; the key name is the pair's alias (or name)
//     ending .rsa.pub, the file apk looks for in /etc/apk/keys.
//   - A local or federated repository's indexes are derived from its
//     packages, never uploaded; a remote caches the upstream's after the
//     item kit's remote source request has checked it.
//   - The index work takes at most 100,000 packages per repository, and
//     an index at most 32 MiB of records.
//   - KindIndex and the property names are stored, so they stay
//     byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks and optional interfaces); decision 0033:
//     docs/research/decisions/0033-persistent-work-queue.md (the index
//     rebuild runs as queued work).
//   - docs/testing/alpine.md (connecting apk, signing keys and the
//     acceptance evidence).
//   - JFrog Alpine Linux repositories:
//     https://docs.jfrog.com/artifactory/docs/alpine-linux-repositories.
//   - The APK v2 package format: https://wiki.alpinelinux.org/wiki/Apk_spec.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 232 (ecosystem/alpine), line 379 (the slice row), line 405 (the
//     protocol's home, ecosystem/alpine/http) and line 997 (the mapping
//     row).
//   - Plan: docs/implementation/plan.md#p29-linux-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/757 and
//     https://github.com/weaveplatform/artifactweave/issues/1041
//
// # Status
//
// Implemented P9c, which delivered the plan's P29 group (issues #757 to
// #760). Moved here in P9d.
//
// # Testing
//
//   - package_test.go and package_validation_test.go: ReadPackage binds an
//     archive to its path and data, and refuses archives apk cannot
//     install, invalid control metadata and unsafe control or data
//     entries.
//   - decode_validation_test.go and index_test.go: index paths, invalid
//     containers and records, native records kept byte for byte, and
//     EncodeIndex's order, refusals and signature over the exact
//     compressed index.
//   - handler_validation_test.go: packages kept apart from generated
//     indexes, rebuilds queued only for a hosted repository's own package
//     changes, and missing or invalid stored packages.
//   - work_validation_test.go: the index work over the current
//     repository, signing through Env.Keys, unusable signing pairs, and
//     derive and removal failures.
//   - contract_test.go: the format contract suite (artifacts/core/formattest)
//     with KindIndex as the repository's work.
//
// The protocol's native apk evidence is the @alpine acceptance partition
// (test/acceptance/features/alpine.feature).
package alpine
