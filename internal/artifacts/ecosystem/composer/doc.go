// Package composer validates hosted Composer archives and their package
// identity. A version is one ZIP archive, stored at
// <vendor>/<package>/<file>.zip, whose single composer.json, at the root or
// in one top-level folder, names the package its path does; its version is
// the file's own or the one composer.version property of the archive's
// item. Composer keeps no index: the protocol builds its catalogs from the
// stored archives on every request, so a delete or restore shows at once.
//
// # Role
//
// artifacts module, ecosystem layer: the Composer archive validation and
// catalog rules of the Composer slice. Its Handler plugs into the item
// service through the format hooks (decision 0032), so every archive is
// checked when it is written; the slice's http package
// (artifacts/ecosystem/composer/http) serves the Composer 1 and 2 catalogs
// from the archives it admitted.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for composer repositories to Handler, and the
//     work queue, which runs every format's reindex through it; and the
//     protocol, which reads stored and upstream archives (ParseArchive),
//     checks names, versions and paths (ValidName, ValidVersion,
//     HostedPath) and renders catalog entries (Package.Document).
//   - Driven: the format hooks' environment (format.Env): its blobs hold the
//     archives, and its store holds the items and their properties. The
//     generic blackout and include and exclude checks
//     (artifacts/ecosystem/generic) run before the format's own.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that read an archive once before its transaction
//     (Inspect), admit only a valid archive at its package's path in a local
//     or federated repository (Admit), and recheck every stored file
//     (Reindex). It writes no index and no property: its Deployed and
//     delete hooks are the generic ones, which do nothing.
//   - The archive rules: ParseArchive, Package and its Document, ValidName,
//     ValidVersion, HostedPath and the bounds (ArchiveLimit,
//     MetadataLimit).
//
// # Invariants
//
//   - An archive is read whole, every entry to its end, within fixed bounds
//     (128 MiB stored, 128 MiB for each inflated entry and 256 MiB in all,
//     4,096 entries and a 1 MiB composer.json), with no absolute,
//     backslashed or dot-segment path.
//   - The lowercase vendor/package name in composer.json is the archive's
//     first two path segments. The file name may differ from the version,
//     which a composer.version property may supply, as JFrog's Composer
//     deployment guide describes, but the property must agree with an
//     embedded version.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, whose upstream archives ParseArchive checks).
//   - docs/operations/composer.md (connecting Composer, the archive rules,
//     the repository classes and reindex).
//   - JFrog PHP Composer repositories:
//     https://docs.jfrog.com/artifactory/docs/php-composer-repositories, and
//     the Composer repository specification:
//     https://getcomposer.org/doc/05-repositories.md.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/composer), line 379 (the slice row) and line 997
//     (the mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/763 and
//     https://github.com/weaveplatform/artifactweave/issues/1047
//
// # Status
//
// Implemented P9c, which delivered the plan's P30 group (issues #762 to
// #768). Moved here in P9d.
//
// # Testing
//
//   - composer_test.go: archive identity and bounds, the hosted path and
//     name grammar, adversarial archives, and Admit's class and
//     cached-write rules.
//   - admission_test.go: admission and reindex of a local repository
//     through the item service.
package composer
