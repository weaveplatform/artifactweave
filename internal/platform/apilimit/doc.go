// Package apilimit combines shared request budgets with bounded local work.
//
// Admission has two halves. Request budgets are fixed one-minute windows
// counted in the metadata store, so replicas that share it share the rates,
// and a request charges all of its budgets or none. Concurrency permits bound
// the work in one process, without a waiting queue: a request that finds no
// free permit is refused at once.
//
// # Role
//
// platform layer: API admission, with request budgets shared across replicas
// and concurrency permits per process. Admission belongs to no module, so it
// sits in the platform. The composition root builds one Service from the api
// section of the configuration, and the router's request-limit middleware
// (app/limits.go) calls it for every request but the health probes: Enter
// and Authentication before credentials are verified, Admit and, for
// expensive work, EnterExpensive once the principal and the resources are
// known, and EnterRecovery on the administrative recovery routes. The
// request budget port and its values are declared here, by the package that
// charges through them, and New takes Store, the one method it calls, so
// that the package imports no store (decision 0044, "Narrowing"); the
// metadata store and the platform's store satisfy it, and the platform's
// store adapters (platform/store/sql and platform/store/inmem) implement
// RequestBudgetStore. The package imports the kernel and the standard
// library.
//
// # Inputs
//
//   - Options, the api section of the configuration, with Default as the
//     initial policy: the per-minute rates for authentication, the whole
//     service, each principal, project and repository, and expensive work in
//     total and per principal; the concurrency limits in total, for expensive
//     work and per principal; the counter capacity, MaxKeys; and the budget
//     and expensive-work timeouts. Validate bounds them even while admission
//     is disabled.
//   - A Store, whose RequestBudgets is the shared budget store, and a clock
//     (time.Now when nil).
//   - Per request, the context, and for Admit a Scope: the authenticated
//     principal, the resolved project and repository, and whether the work is
//     expensive.
//
// # Outputs
//
//   - Service, with Enter, EnterExpensive, Authentication, Admit,
//     EnterRecovery and Options; New returns a nil Service when admission is
//     disabled.
//   - Decision, the caller's own quota and when it resets, which the HTTP
//     boundary turns into rate limit headers; Scope; the sentinels ErrInvalid
//     and ErrBusy.
//   - The port: RequestBudgetStore, with RequestBudget, BudgetRequest and its
//     Validate, BudgetBalance, BudgetDecision and ErrBudgetCapacity.
//
// # Invariants
//
//   - Backend errors never grant admission, and there is no per-process
//     fallback: a budget store that fails or misses BudgetTimeout refuses the
//     request.
//   - A refusal charges none of the budgets, so a rejected request uses up no
//     other principal's allowance.
//   - Budget keys are SHA-256 digests of server-owned policy names and
//     authenticated scope, never client labels.
//   - Permits are released only after the handler returns, also when its
//     context is canceled; a release function acts once however often it is
//     called.
//   - The recovery reserve does not depend on budget capacity: its two
//     concurrent requests and sixty attempts a minute are counted in this
//     process's memory.
//
// # References
//
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md
//     ("Incoming request budgets and expensive work": the policy, its
//     defaults and its failure modes; "Temporary restrictions and
//     administrative recovery": the recovery reserve).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Narrowing"; platform/apilimit is one of the new packages the target
//     layout does not name).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 127 (the platform tree, which does not list this package) and
//     lines 40 and 41 (the platform imports no module and no gateway).
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     PLT (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/890
//   - https://github.com/weaveplatform/artifactweave/issues/987
//
// # Status
//
// Implemented with decision 0043 (issue 890, pull request 905) and released
// in v0.2.0, the P9c release: request budgets shared through the metadata
// store, which declared the port; per-process and expensive permits without
// a waiting queue; and the recovery reserve. Moved here in P9d: New takes
// Store, and the port and its values are declared here.
//
// # Testing
//
//   - limit_test.go: TestUnitSharedRatesAndScopes (two services share rates
//     through one store; the principal, expensive, project and repository
//     budgets; the window reset), TestUnitBoundedConcurrency (ordinary,
//     expensive and per-principal permits, releases that act once, and
//     canceled contexts), TestUnitBudgetFailureIsClosedAndBounded (a stalled
//     budget store refuses within BudgetTimeout) and
//     TestUnitAPIPolicyValidation (the bounds, a missing store and disabled
//     admission).
//   - recovery_test.go: TestUnitRecoveryReservedAllowance (two concurrent
//     requests and sixty a minute, the window reset and a canceled context).
//
// The tests build their services over the platform's in-memory store. The
// platform's request budget contract (the RequestBudgets and
// RequestBudgetBoundedPruning subtests of platform/store/storetest) runs on
// the platform's in-memory and SQLite stores and on the metadata store's
// in-memory, SQLite and PostgreSQL stores, the router's request limit tests
// (app/limits_test.go) run against this package.
package apilimit
