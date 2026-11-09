// Package gems validates RubyGems archives and renders the indexes RubyGems
// and Bundler read. A gem is one archive, stored at
// gems/<name>-<version>[-<platform>].gem, whose embedded gemspec names the
// gem, its version, its platform and its runtime dependencies; that
// gemspec, read from the stored bytes, is what indexes the version and
// fills the compact index (names, versions and info/<name>) and the legacy
// Marshal 4.8 indexes (specs.4.8.gz, its latest and prerelease forms, and
// quick/Marshal.4.8/<file>.gemspec.rz). A local or federated repository
// stores no index file: the protocol renders them from the stored gems.
//
// # Role
//
// artifacts module, ecosystem layer: the RubyGems package, index and
// Marshal rules of the RubyGems slice. Its Handler plugs into the item
// service through the format hooks (decision 0032), so every write in a
// gems repository reaches it whichever API carried it; the slice's http
// package (artifacts/ecosystem/gems/http) serves the RubyGems and Bundler
// protocol over what it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for gems repositories to Handler, and the work
//     queue, which runs every format's reindex through it (JFrog's
//     /api/gems/{repoKey}/reindex schedules one); and the protocol, which
//     reads pushed, stored and upstream gems (Read, MaxGemBytes), lists a
//     repository's stored files (Files), checks names and paths
//     (ValidName, ValidPath, Filename), renders the indexes (CompactNames,
//     CompactVersions, CompactInfo, InfoMD5, Specs and QuickSpec) and
//     checks an upstream's compact index before a remote caches it
//     (ParseCompactVersions, ParseCompactInfo, VersionToken and
//     MaxIndexBytes).
//   - Driven: the format hooks' environment (format.Env): its blobs hold
//     the gems, and its store holds the items and the package index
//     (artifacts/core/pkgindex), which Reindex rebuilds from an AQL search
//     of the stored files (Files). The generic deploy policy
//     (artifacts/ecosystem/generic), which Handler embeds, is the first
//     check Admit applies.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that read a gem once before its transaction
//     (Inspect), admit only a valid gem whose identity gives its path and
//     keep the index paths out of local and federated repositories
//     (Admit), link a stored gem to its version (Deployed), class the index
//     paths as derived content (Classify) and rebuild the index from the
//     stored gems (Reindex). Deletes need no hook: the item service drops a
//     deleted gem's link and prunes a version left without files.
//   - The package index: a package per gem name, with a version per version
//     string, whose files are its gems for every platform.
//   - The rules the protocol shares: Package, Dependency, Read, ValidName,
//     ValidVersion, ValidPath, Filename, DerivedPath, Files,
//     CompareVersions, the index renderers and parsers with
//     CompactVersionEntry, Marshal, GzipMarshal and the bounds
//     (MaxGemBytes and MaxIndexBytes).
//
// # Invariants
//
//   - A gem is read whole within fixed bounds (256 MiB, 2 MiB for its
//     unpacked gemspec and checksums, and four times 256 MiB for its
//     unpacked data) before its gemspec is trusted. It is a TAR holding
//     metadata.gz and data.tar.gz, and optionally checksums.yaml.gz, each a
//     regular file once; the data holds only regular files, folders and
//     symlinks, with no absolute or traversing path or link target; the
//     checksums, when present, give the SHA-256 of both other parts; and the
//     gemspec is a YAML Gem::Specification.
//   - A gem's name, version and platform give its path, with no platform
//     suffix for ruby; only its runtime dependencies are indexed.
//   - The index paths (DerivedPath) of a local or federated repository are
//     generated, so a write to one is refused (409).
//   - An upstream's compact index is taken only in bounded, complete rows
//     (32 MiB a file) whose names, versions, platforms, dependencies and
//     SHA-256 checksums validate; a yanked version (a token starting "-")
//     leaves the versions list.
//   - The index is derived from the stored gems, never from a client's
//     claim.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, whose upstream index and gems this package checks).
//   - docs/testing/gems.md (connecting gem and Bundler, the archive and
//     index checks, and the evidence).
//   - JFrog RubyGems repositories:
//     https://docs.jfrog.com/artifactory/docs/rubygems-repositories, and the
//     RubyGems compact index specification:
//     https://guides.rubygems.org/rubygems-org-compact-index-api/.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/gems), line 379 (the slice row) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/764 and
//     https://github.com/weaveplatform/artifactweave/issues/1049
//
// # Status
//
// Implemented P9c, which delivered the plan's P30 group (issues #762 to
// #768). Staged in P9d.
//
// # Testing
//
//   - package_test.go: the pinned rake gem, the archive bounds, unsafe
//     data, malformed archives and gemspecs, optional gemspec shapes,
//     runtime dependencies, the compact index round trip and version order;
//     and Ruby loading the Marshal indexes and gemspecs
//     (TestAccRubyClientLoadsMarshalIndexes,
//     TestAccRubyClientLoadsRichGemspec and
//     TestAccRubyClientLoadsDefaultPlatformAndRequirements).
//   - coverage_test.go: live versions and yanks, dependencies and
//     platforms, malformed wire rows, Ruby's version order, admission that
//     protects the generated indexes and a gem's identity, reindex of the
//     stored gems, and latest_specs.4.8.gz keeping a release when a newer
//     prerelease exists (TestAccRubyLatestSpecsRetainsStableWhenPrereleaseIsNewer).
//
// The Ruby tests skip unless ruby is installed.
package gems
