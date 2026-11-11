// Package storekit holds the assertions and paging helpers that every store
// contract suite shares: a value read back compared with what a store returns
// for the value written, fixed instants to store, checks of a call's error,
// and a reader of every page of a list.
//
// # Role
//
// platform layer, test support (platform/testkit): the assertions and paging
// helpers shared by every module's store contract suite, so that each suite
// states its expectations in the same terms and every backend fails them with
// the same messages. Test files in any module may import it (dependency rule
// 1); production code never does. It imports the kernel, go-cmp and the
// standard library.
//
// # Inputs
//
//   - The calling test's *testing.T, and the values and errors to check.
//   - For Pages: a page size and a list function over kernel.Page that
//     returns a page, the next cursor and an error.
//   - Driving: the store contract suites (decision 0015), the composite
//     store's today and each module's from STO-12 to STO-18.
//
// # Outputs
//
//   - Opts, the go-cmp options stored values are compared with.
//   - Base and At, the instants a test stores.
//   - Want, what a store returns for a value, and Same, which stops the test
//     when a value read back differs from it.
//   - Must, WantErr and Get, which stop the test on an error it did not
//     expect or without the error it did.
//   - Pages, every item of a paged list in order.
//
// # Invariants
//
//   - Same compares with Want, kernel.Normalise of the value written: every
//     backend returns times in UTC truncated to microseconds and durations
//     truncated to milliseconds, the precision of the SQL columns, so a
//     backend that keeps more precision, or loses some, fails.
//   - Opts treats empty and nil collections as equal and requires times to be
//     the same instant in the same location, so a backend that returns local
//     times instead of UTC fails.
//   - Base is in a non-UTC zone with sub-microsecond digits, so every test
//     that stores a time also checks UTC and precision normalisation.
//   - Pages fails the test when a page holds more items than were asked for,
//     and when a list has not ended after 10,000 pages, so a cursor that never
//     moves fails instead of hanging the run.
//   - Every check stops the test at its first failure (t.Fatal), with the
//     messages the contract suites have always printed.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (one contract suite defines the behaviour of every backend).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Package placement and names": platform/testkit/storekit, a package the
//     target document does not name; "Persistence": the contract suites;
//     "Names the migration introduces"; "Dependency rules").
//   - Plan: docs/implementation/plan.md#p2-stores and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/996
//
// # Status
//
// Implemented P2: the helpers came with the metadata store's contract suite
// (pull request 3). Moved here in P9d: exported for the module suites (issue
// 996).
//
// # Testing
//
// The package has no tests of its own. The composite store's contract suite
// checks with every helper, so its runs exercise each one's passing path:
// TestUnitContract on the in-memory store, and TestAccContractFile,
// TestAccContractMemory and TestAccContract on the SQL stores.
package storekit
