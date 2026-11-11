// Package onemodel will be the OneModel GraphQL gateway: one schema stitched
// from the modules' subgraphs.
//
// # Role
//
// gateway layer: a driving adapter that owns no data. Its resolvers call
// module api packages only (decision 0044, dependency rules).
//
// # Inputs
//
// None yet. Planned: OneModel GraphQL queries, answered through module api
// packages such as evidence/api (target layout, lines 365 and 970).
//
// # Outputs
//
// None yet. Planned: permission-filtered results across the implemented
// subgraphs, with defined partial errors (plan P26, task 4 and exit
// criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 99 and 365, and "Adding a new business domain: Evidence
//     (P23)", line 970.
//   - Plan:
//     docs/implementation/plan.md#p26-security-scanning-and-intelligence.
//   - Context map: the gateway is not one of its thirteen rows; the target
//     layout's module status table gives its phases (line 99).
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P26.
package onemodel
