// Package gateway will root the driving adapters that own no data: the
// OneModel GraphQL gateway and the web console.
//
// # Role
//
// gateway layer. A gateway package imports only module api packages, kernel
// and platform/httpkit (decision 0044, dependency rules), and declares no
// migrations (target layout, lines 37-39).
//
// # Inputs
//
// None yet. Planned: the module api packages that onemodel and console call.
//
// # Outputs
//
// None yet. The target layout names no code for this directory itself (lines
// 364-366); its subpackages are onemodel (P26) and console (P39).
//
// # References
//
//   - Context map: the gateway is not one of its thirteen rows; the target
//     layout's module status table gives its phases (line 99).
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 99 and 364-366.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//   - Plan:
//     docs/implementation/plan.md#p26-security-scanning-and-intelligence,
//     #p39-web-console.
//
// # Status
//
// Placeholder: implemented by P26, P39.
package gateway
