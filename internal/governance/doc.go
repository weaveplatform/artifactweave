// Package governance will be the governance module: applications, Unified
// Policy, control points, approvals and audit history.
//
// # Role
//
// governance module, module root. Only app imports a module root; other
// modules reach governance through governance/api (decision 0044, dependency
// rules).
//
// # Inputs
//
// None yet. Planned: the Deps that app passes to New. Plan P25 integrates the
// evidence and release models, which this module reaches only through
// evidence/api and releases/api.
//
// # Outputs
//
// None yet. Planned: module.go with New(Deps), and the module's gate, which
// app registers on artifacts (target layout, lines 713-714 and 801-802). The
// module's layers are its subpackages: api and core/application, core/policy,
// core/controlpoint, core/approval and core/audit.
//
// # References
//
//   - Context map, governance row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns applications, Unified Policy, control points, approvals and audit
//     history; phases P25.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 85-104 and 357.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders" and "Composition and HTTP".
//   - Plan: docs/implementation/plan.md#p25-apptrust-and-policy-governance.
//
// # Status
//
// Placeholder: implemented by P25.
package governance
