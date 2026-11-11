// Package upstreamtest gives tests the upstream transport's pool and
// credential resolver without importing the transport: core remote's and
// Smart Remote's tests may not import their own module's adapters
// (decision 0044, dependency rule 8), but test files may import any test
// support package (rule 1).
//
// # Role
//
// artifacts module, test support for the upstream adapter
// (artifacts/adapters/upstream). Only test files import it.
//
// # Inputs
//
//   - testing.TB, whose cleanup closes a pool and whose Fatal reports a
//     resolver that cannot be built.
//   - A store and a revision key, for NewResolver.
//
// # Outputs
//
//   - NewPool: an empty *upstream.Pool, closed when the test ends, which a
//     test holds as core remote's Upstream.
//   - NewResolver: an *upstream.Resolver over the store with no keyring,
//     whose Options method a test installs as Policy.Options.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules" (rules 1 and 8) and the remote split, which names
//     this package.
//   - https://github.com/weaveplatform/artifactweave/issues/1026
//
// # Status
//
// Implemented P9 (the pool and resolver it builds). Moved here in P9d.
//
// # Testing
//
// Its callers, the core remote and Smart Remote tests, exercise it; it has
// no tests of its own and is excluded from the coverage gate, since its
// only other branch is the Fatal of an invalid revision key.
package upstreamtest
