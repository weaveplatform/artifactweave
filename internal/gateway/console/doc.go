// Package console will serve the web console and its static assets over the
// public APIs.
//
// # Role
//
// gateway layer: a driving adapter that owns no data. It uses the same public
// APIs as automation, so the console holds no configuration of its own (plan
// P39, task 2).
//
// # Inputs
//
// None yet. Planned: browser requests, in the sessions of the access module
// (plan P21), served through module api packages (target layout, line 366).
//
// # Outputs
//
// None yet. Planned: repository, artifact, search, identity, project,
// configuration, release, evidence and policy workflows, and operator views
// (plan P39, tasks 1 and 2).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 99 and 366.
//   - Plan: docs/implementation/plan.md#p39-web-console.
//   - Context map: the gateway is not one of its thirteen rows; the target
//     layout's module status table gives its phases (line 99).
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P39.
package console
