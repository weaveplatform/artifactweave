// Package apptest supplies the stores that the package format contract
// runs on, which only the composition root may build: FormatBackends hands
// artifacts/core/formattest the in-memory, SQLite and PostgreSQL composite
// stores, so that the contract suite opens no store of its own and artifacts
// core never imports app.
//
// # Role
//
// app, test support: the composition root's backends for the format
// contract suite. Test files of any module may import it (dependency rule
// 1); production code never does.
//
// # Inputs
//
//   - The test each backend opens a store for, for its temporary directory,
//     its cleanup and its failures.
//   - ARTIFACTWEAVE_TEST_POSTGRES_DSN, a PostgreSQL server to use in place
//     of the container that the module SQL harness starts.
//
// # Outputs
//
//   - FormatBackends: the in-memory composite ("inmem"), the SQLite
//     composite in a file ("sqlite") and the PostgreSQL composite on the
//     server the test binary shares ("postgres"), in that order, each opened
//     as the item service's store.
//
// # Invariants
//
//   - It imports formattest, which imports the format registry and through
//     it every slice, so only external test packages import it: a slice's
//     in-package test that did would be an import cycle.
//   - Each backend opens a fresh, empty store for each scenario: the
//     PostgreSQL one empties the shared server's composite tables first, and
//     skips in -short runs and where Docker cannot run Linux containers.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md,
//     "Verification" (the format contract on every store).
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (one contract suite for every backend; PostgreSQL through
//     testcontainers).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": formattest.Run takes its backends as a parameter;
//     "Dependency rules", rule 1).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 198 (core/formattest) and "Tests and enforcement" at lines 842 to
//     901.
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     STO (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1098
//
// # Status
//
// Implemented P9d: the backends of the format contract, which P7 added on
// the three stores (https://github.com/weaveplatform/artifactweave/issues/268),
// taken out of the suite (https://github.com/weaveplatform/artifactweave/issues/1098).
//
// # Testing
//
// It has no tests of its own: every format's contract test runs through
// FormatBackends, and its remaining branches are t.Fatal paths taken only
// when a store cannot be opened.
package apptest
