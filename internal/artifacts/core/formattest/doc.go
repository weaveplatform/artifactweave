// Package formattest is the contract suite for package formats (decision
// 0032). Every format runs Run with a fixture that builds packages of its
// type, and one set of scenarios proves, on the in-memory, SQLite and
// PostgreSQL stores, that its package index (decision 0018) stays right on
// every write path the item service offers: deploy, overwrite, delete, copy,
// move, restore from the trash can, property changes and a remote
// repository's cache.
//
// # Role
//
// artifacts module, test support: the format contract suite every slice
// runs. Only tests import it, which is what lets it import the format
// registry, artifacts/ecosystem, to pick a fixture's format (decision 0044,
// "Artifacts and remote"). The test files of any module may import it
// (dependency rule 1).
//
// # Inputs
//
//   - A slice's handler and repository configuration, as a Format: the
//     package type of the repositories the suite creates (Type), their
//     layout (Layout), any change the format needs in each local one
//     (Local, as Terraform's terraformType chooses modules or providers,
//     decision 0040), and the Handler, where nil means the one
//     ecosystem.For picks for Type.
//   - The fixture's packages (Version): the suite names packages "alpha"
//     and "beta" and versions "1.0" and "2.0", and the fixture maps them to
//     its ecosystem's names and files. Properties are the property sets the
//     suite applies to a version's files in turn, with the properties the
//     format's index follows; Work lists the kinds of work the format
//     queues keyed by package, as npm's, Go's and Maven's are.
//   - Two options of the fixture cover what P8's formats add (decision
//     0032, "Verification"). The first, Format.RepoWork, lists the kinds of
//     derived work a format keys by repository, such as Helm's index.yaml
//     (decision 0039). Every write that changes a repository's versions
//     must leave exactly one item of each, keyed by the repository's key,
//     however many packages it touched: after a deploy, an overwrite, a
//     copy (its target), a move (both repositories), a delete and a
//     restore.
//   - The second, File.SidecarOf, marks a file as a sidecar its format
//     keeps with its owner (format.SidecarKeeper), such as a Helm chart's
//     .prov. A sidecar is never a version file. It follows its owner
//     through a delete, a copy, a move and a trash restore. On its own it is
//     an ordinary file that may arrive before its owner. A format that
//     records its sidecars in a version's metadata has that compared after
//     a reindex from empty too.
//   - Format.NoRemoteCache marks a format whose index does not cover a
//     remote repository's cache; the cache scenario passes it unchecked.
//
// # Outputs
//
//   - Run: subtests over a seeded world, one for each backend the caller
//     passes and each scenario (sqlite/move, for example). Each scenario
//     gets a fresh store from its backend, seeded with three empty local
//     repositories of the format's type, and the item and trash services
//     over them, with a clock that moves a minute on every write. A scenario
//     that finds the contract broken fails its subtest with what differs; a
//     fixture that contradicts itself, or an empty list of backends, fails
//     Run before any scenario runs.
//   - Backend: a named opener of a fresh store, as the item service's store
//     (item.Store), which the world's item and trash services run over. The
//     suite opens no store itself: app/apptest.FormatBackends gives the
//     formats the in-memory, SQLite and PostgreSQL composite stores, and
//     NuGet's remote reindex test and the Terraform state service's tests
//     run their own cases on the same stores.
//
// # Invariants
//
//   - "Right" has two halves. The fixture says which versions a scenario
//     leaves and which files each links. And the item tree is the source of
//     truth, so the index the hooks kept must be the one the format's
//     reindex reads from the tree: a reindex over it changes nothing, and a
//     reindex from an empty index rebuilds it, every version's metadata
//     included, but for what only history knows: a package's first push and
//     a version's last download. A format that indexes nothing must leave
//     the index empty.
//   - The suite drives the item service, whose entry points every API
//     calls: the properties API and AQL properties.update call
//     Service.Annotate, which TestAccPropertyWritesReachTheFormat in
//     test/server/artifacts holds them to. A format that passes therefore
//     keeps its index whichever API carries the write.
//   - Run holds the fixture to its own rules before any scenario: a kind of
//     work is keyed either by package or by repository, a sidecar's owner is
//     another file of its version that is no sidecar itself, and no sidecar
//     is linked.
//   - Every importer is an external test package, such as npm_test: this
//     package imports the registry, which imports every slice, so a slice's
//     in-package test that imported it would be an import cycle.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md,
//     "Verification" (the suite, and the two fixture options P8 added).
//   - Decision 0018: docs/research/decisions/0018-package-index.md (the
//     package index the suite holds to the item tree).
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (contract suites and the coverage gate).
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md
//     (the work a format queues, and the key it is queued under).
//   - Decisions 0039 (Helm: index.yaml per repository, and the .prov
//     sidecar) and 0040 (Terraform: terraformType, which Format.Local sets).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Artifacts and remote" (formattest may import the registry),
//     "Persistence" (Run takes its backends as a parameter) and
//     "Dependency rules" (rule 1).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 198 (core/formattest, the contract suite every slice runs), line
//     999 (the mapping row) and "Tests and enforcement" at lines 842 to 901.
//   - Plan: docs/implementation/plan.md#p7-npm-go-maven-locals,
//     #p8-pypi-nuget-helm-terraform-locals and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/268,
//     https://github.com/weaveplatform/artifactweave/issues/533,
//     https://github.com/weaveplatform/artifactweave/issues/972 and
//     https://github.com/weaveplatform/artifactweave/issues/1088
//
// # Status
//
// Implemented P7 and P8. P7, format framework v2, added the suite with its
// scenarios on the three stores
// (https://github.com/weaveplatform/artifactweave/issues/268), and the cache
// scenario with NoRemoteCache
// (https://github.com/weaveplatform/artifactweave/issues/361). P8 added
// Format.RepoWork and File.SidecarOf
// (https://github.com/weaveplatform/artifactweave/issues/533), and
// Format.Local for Terraform's terraformType. Moved here in P9d, which
// narrowed the backends and the world to the item service's store
// (https://github.com/weaveplatform/artifactweave/issues/1088), and Run takes
// its backends from its caller instead of opening them, so that artifacts
// core never imports app
// (https://github.com/weaveplatform/artifactweave/issues/1098).
//
// # Testing
//
//   - The package is excluded from coverage (.testcoverage.yml), as the
//     other contract-suite harnesses are (decision 0015): its remaining
//     branches are contract-violation failures, which a format that keeps
//     the contract never reaches.
//   - TestUnitTaggedFormatKeepsTheContract,
//     TestUnitRepositoryWorkKeepsTheContract,
//     TestUnitSidecarsKeepTheContract and
//     TestUnitRepositoryWorkAndSidecarsKeepTheContract: a format that keeps
//     the contract passes Run, with either fixture option and with both, on
//     the three composite stores, which the tests build themselves because
//     app/apptest imports this package.
//   - TestUnitTheSuiteCatchesBrokenFormats: each way of breaking the
//     contract fails the scenario that guards it.
//   - TestUnitInconsistentFixturesFail: a fixture that contradicts itself
//     fails before any scenario runs.
//   - TestUnitAnIndexWithoutAReindexerFails: a format that indexes nothing
//     passes only while the index stays empty.
package formattest
