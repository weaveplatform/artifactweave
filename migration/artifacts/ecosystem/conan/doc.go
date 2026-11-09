// Package conan owns Conan's on-disk revision layout and write policy.
// A recipe revision's files are stored under
// user/name/version/channel/recipe-revision/export/ and a binary package
// revision's under recipe-revision/package/id/package-revision/, the
// conan-default layout Artifactory documents (ParseStored). A revision is
// committed once its conanmanifest.txt is stored, and only committed
// revisions enter the package index.
//
// # Role
//
// artifacts module, ecosystem layer: the Conan revision layout and write
// policy of the Conan slice. Its Handler plugs into the item service
// through the format hooks (decision 0032); the slice's http package
// (artifacts/ecosystem/conan/http) serves the Conan revision protocol over
// what it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for conan repositories to Handler, and the work
//     queue, which runs every format's reindex through it; and the
//     protocol, which builds and parses revision paths (Reference,
//     FilePath, ParseStored, Component, SafeFile), lists a repository's
//     folders (Files) and marks its own writes (Native).
//   - Driven: the format hooks' environment (format.Env), through whose
//     store Handler lists a repository's files and keeps the package index
//     (artifacts/core/pkgindex); and the generic deploy policy
//     (artifacts/ecosystem/generic), which Handler embeds and applies
//     first.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that admit a revision file only from the native
//     protocol and only in the layout (Admit), and rebuild the index after
//     a revision file is stored or removed (Deployed, Deleted, Reindex).
//   - The package index: a package per name@user/channel, with a version
//     per recipe version whose files are those of its committed recipe and
//     package revisions.
//   - The layout rules the protocol shares: Manifest, Reference, FilePath,
//     ParseStored, Component, SafeFile, Files, Native and IsNative.
//
// # Invariants
//
//   - A generic item write cannot create a Conan revision by choosing its
//     storage path: only a write the protocol marked Native, or a remote's
//     cached write, is admitted.
//   - The index lists only committed revisions: a recipe revision with its
//     manifest, and a package revision with its own and its recipe
//     revision's.
//   - A path component is refused before it becomes an item path if it
//     could traverse or carries a control character.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks; the protocol and storage decision); decision 0005:
//     docs/research/decisions/0005-authentication-and-tokens.md (the login
//     tokens the slice's protocol issues).
//   - docs/testing/conan.md (connecting Conan 2 and Conan 1, the commit
//     marker, remote and virtual repositories, and the qualification).
//   - JFrog Conan repositories:
//     https://docs.jfrog.com/artifactory/docs/conan-repositories, and the
//     repository client support matrix:
//     https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/conan), line 379 (the slice row) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p31-native-build-and-plugin-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/770 and
//     https://github.com/weaveplatform/artifactweave/issues/1038
//
// # Status
//
// Implemented P9c, which delivered the plan's P31 group (issues #770 to
// #775). Staged in P9d.
//
// # Testing
//
//   - path_test.go: the stored layout's recipe and package paths, and the
//     components and file names it refuses.
//
// The hooks are also tested end to end through the protocol, from the
// server root's Conan suites and internal/app's (docs/testing/conan.md).
package conan
