// Package bower validates Bower package trees and indexes versions from
// their bower.json manifest. A hosted version's files live in one tree,
// <name>/<version>/, and its bower.json, whose name and version must match
// that path, is what indexes the version. The item tree remains the source
// of truth.
//
// # Role
//
// artifacts module, ecosystem layer: the Bower package tree validation and
// index of the Bower slice. Its Handler plugs into the item service
// through the format hooks (decision 0032); the slice's http package
// (artifacts/ecosystem/bower/http) serves the Bower registry and
// bower-art-resolver protocol over what it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for bower repositories to Handler, and the work
//     queue, which runs every format's reindex through it; and the
//     protocol, which checks names, versions and paths (ValidName,
//     ValidVersion, ParsePath, ManifestPath) and decodes the manifests it
//     serves and fetches (DecodeManifest, DecodeGitManifest,
//     MaxManifestBytes).
//   - Driven: the format hooks' environment (format.Env), whose blobs hold
//     the manifests and through whose store Handler lists a repository's
//     manifests and keeps the package index (artifacts/core/pkgindex); and
//     the generic deploy policy (artifacts/ecosystem/generic), which
//     Handler embeds and applies first.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that admit a file only inside a versioned package
//     tree and a bower.json only when it matches its path (Inspect,
//     Admit), index a version once its bower.json is stored (Deployed) and
//     rebuild the index from the stored manifests (Reindex). Deletes need
//     no hook: the item service prunes a version whose manifest is gone.
//   - The package index: a package per name, with a version per stored
//     bower.json, whose file is that manifest.
//   - The rules the protocol shares: Manifest, DecodeManifest,
//     DecodeGitManifest, ValidName, ValidVersion, ParsePath, ManifestPath
//     and MaxManifestBytes.
//
// # Invariants
//
//   - A hosted file's path is <name>/<version>/<file>, with a valid name,
//     a semantic version, and a file path with no empty, . or .. segment
//     and no colon.
//   - A bower.json is one JSON object of at most MaxManifestBytes with no
//     repeated key, and its name and version match its path; only a
//     manifest fetched from a Git tag may omit the version, which the tag
//     supplies.
//   - The index is derived from the stored manifests, never from a
//     client's claim.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks; the protocol and storage decision); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/bower.md (connecting the Bower client and
//     bower-art-resolver, publishing a version tree, and the evidence).
//   - JFrog Bower repositories:
//     https://docs.jfrog.com/artifactory/docs/bower-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 232 (ecosystem/bower), line 379 (the slice row) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/768 and
//     https://github.com/weaveplatform/artifactweave/issues/1040
//
// # Status
//
// Implemented P9c, which delivered the plan's P30 group (issues #762 to
// #768). Moved here in P9d.
//
// # Testing
//
//   - bower_test.go: manifest identity and package paths.
//   - security_test.go: ambiguous, unsafe and interrupted manifests,
//     admission from the stored manifest, corrupt stored manifests, and
//     reindex order, validation and cancellation.
package bower
