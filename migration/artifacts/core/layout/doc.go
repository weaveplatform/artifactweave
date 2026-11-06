// Package layout implements Artifactory repository layouts: the path patterns
// that tell the server which part of an artifact path is the organisation,
// module, base revision, integration revision, classifier and extension. A
// layout drives version search, snapshot cleanup and cross-layout copy, so it
// has to parse and build paths exactly as Artifactory does.
//
// # Role
//
// artifacts module, core layer: the repository layout engine. The layout
// searches, copy and move, the Maven format and remote discovery read and
// build paths through it. It imports only the standard library, so it never
// reaches the module's adapters or an ecosystem http package (decision 0044,
// dependency rules).
//
// # Inputs
//
//   - Layout definitions from repository configuration: a repository names
//     its layout in repoLayoutRef (decision 0003), and the configuration
//     descriptor's repoLayouts hold the definitions, each of which the
//     descriptor compiles before it stores it.
//   - The bundled set: the layouts Artifactory 7.x ships, in the order its
//     descriptor lists them. Bundled returns their definitions and Lookup
//     compiles one by name.
//   - Repository-relative paths to read, and token values (Values) to build
//     paths from.
//
// # Outputs
//
//   - Compiled layouts: Compile and Lookup turn a Definition into a Layout,
//     and VersionTokens gives the Layout that also reads [RELEASE] and
//     [INTEGRATION] in a download path.
//   - Coordinates: Match reads a path's token values and Read its
//     Coordinates, the module identity that the layout searches and the
//     format handlers work with. Build and BuildDescriptor turn values back
//     into a path.
//   - Folder prefixes: Prefixes, the text that every path a layout matches
//     with the given values starts with, which narrows a search before Match
//     decides; and FolderPrefixes, the complete folder those values fix,
//     which remote discovery lists.
//   - Translations between layouts: Translate, for a copy or move between
//     repositories of different layouts, and RemotePath, for a remote
//     repository whose layout differs from its upstream's. Both answer a
//     *LossError, which wraps ErrLossy, when a path has no lossless
//     translation, so two paths never land on one.
//   - Errors: ErrPattern, ErrNoMatch, ErrMissingValue and ErrUnbuildable say
//     why a pattern does not compile, a path does not read, or values do not
//     build a path.
//
// # Invariants
//
//   - Token expressions, pattern parsing and the bundled definitions follow
//     Artifactory's (RepoLayoutUtils in 6.20, 7.x's descriptors), so a path
//     reads here as Artifactory reads it.
//   - A translation never lands two source paths on one target path, or one
//     version would overwrite another.
//   - Compile refuses a token that is neither documented nor custom. Only
//     Lookup's bundled layouts drop one, as Artifactory does, and a pattern
//     with a dropped token builds no path (ErrUnbuildable).
//   - Error texts reach clients, so they stay byte-identical: Compile's when
//     a configuration descriptor change brings a malformed layout, and a
//     LossError's in copy and move results.
//
// # References
//
//   - https://docs.jfrog.com/artifactory/docs/repository-layouts.md, the
//     pattern syntax.
//   - Research 03: docs/research/03-repository-configuration-model.md,
//     "Repository layouts", "Bundled definitions" and "Layout translation".
//   - Decision 0003: docs/research/decisions/0003-repository-model.md (a
//     repository names its layout, and the layout a create without one
//     gets).
//   - Decision 0012: docs/research/decisions/0012-search-and-aql.md, lines
//     333 to 335 (GAVC, versions and latest version read coordinates through
//     each repository's layout).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 212 (core/layout), line 1053 (published through api.Layouts,
//     which https://github.com/weaveplatform/artifactweave/issues/1009 adds)
//     and rule 2 at lines 35 to 36.
//   - Plan: docs/implementation/plan.md#p0-foundations (task 3),
//     #p5-full-aql-searches-build-info, #p7-npm-go-maven-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/968
//
// # Status
//
// Implemented P0 to P9: the pattern engine and the bundled layouts, with
// Bundled and Lookup, in P0; Has and Prefixes for the layout searches in P5;
// Coordinates, Read and VersionTokens, the bundled layouts checked against
// 7.x with Lookup dropping undocumented tokens, Translate and ErrUnbuildable
// in P7; FolderPrefixes and RemotePath for remote discovery in P9. Staged in
// P9d.
//
// # Testing
//
//   - TestUnitMavenMatch, TestUnitMavenBuild, TestUnitOptionalGroupNesting,
//     TestUnitCompileErrors, TestUnitOrgPath, TestUnitHas and
//     TestUnitPrefixes: the pattern engine.
//   - TestUnitBundledCompile, TestUnitBundledNames and the
//     TestUnitBundledLayouts* pair: every bundled layout compiles, is listed
//     in 7.x's order, and reads the real paths it describes and no others.
//     TestUnitBuildCarriesItsValues and TestUnitBuildRefusalText: a build
//     changes with each value, or is refused.
//   - TestUnitRead, TestUnitVersionTokens and TestUnitReadNeedsOrganisation:
//     coordinates.
//   - The TestUnitFolderPrefixes* pair: folder prefixes.
//   - TestUnitTranslate, TestUnitTranslateKeeps, TestUnitLossErrorText,
//     TestUnitTranslateWithSimpleDefault and TestUnitTranslateNeverMerges,
//     which tries every ordered pair of bundled layouts: translations.
//   - The TestUnitRemotePath* pair: remote paths.
package layout
