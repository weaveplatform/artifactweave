// Package api will be the notifications module's published interface: webhook
// administration and the module's events.
//
// # Role
//
// notifications module, api layer. It may import only kernel, platform/outbox
// and its own api subpackages (decision 0044, dependency rules).
//
// # Inputs
//
// None yet. An api package declares contracts; its callers are other modules
// and the gateways.
//
// # Outputs
//
// None yet. Planned (target layout, line 319): notifications.go, the Webhooks
// administration contract, and events.go, with the DeliveryFailed event.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 319.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication.
//   - Context map: the notifications row, restated in the module root's
//     doc.go; https://github.com/weaveplatform/artifactweave/issues/958 holds
//     the map.
//   - Decision 0011: docs/research/decisions/0011-webhooks.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P11.
package api
