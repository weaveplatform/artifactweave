// Package clienttest supplies the pinned clients' process and cache isolation
// shared by single-instance acceptance and multi-instance qualification.
//
// # Role
//
// Test support outside the production modules. Acceptance and peer scenarios
// share command construction and the existing Maven tool-supply policy here.
//
// # Inputs
//
// Explicit fixture directories, command arguments and cancellation contexts;
// pinned client images/binaries and the checksum-verified Maven tool supply.
//
// # Outputs
//
// Commands with private client state, verified tool-only Maven repositories,
// and Maven arguments that keep product heads separate from the read-only tail.
// Callers execute commands and own assertions, diagnostics and resource cleanup.
//
// # Invariants
//
// Containers preserve loopback URLs with host networking and use the runner's
// identity. Client caches and credentials belong to each fixture. Maven tools
// never contain product artifacts. This package changes no production API.
//
// # References
//
// docs/research/decisions/0015-testing-and-acceptance.md;
// docs/testing/ci.md; docs/implementation/plan.md, P13 QUA-03.
//
// # Status
//
// Implemented P13 test support. Integrated phase acceptance remains separate.
package clienttest
