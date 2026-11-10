// Package notificationshttp will serve the event service's endpoints: webhook
// subscriptions, test deliveries and troubleshooting.
//
// # Role
//
// notifications module, adapter layer (HTTP). The package is named
// notificationshttp so that it does not shadow net/http.
//
// # Inputs
//
// None yet. Planned: requests to the event specification's operations,
// through the generated strict server in platform/gen/event (target layout,
// lines 735 and 812).
//
// # Outputs
//
// None yet. Planned: the module's part of that API, composed into the app
// router as a partial strict server (decision 0044, composition and HTTP).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 323, "Webhooks and events (P11, P13)", line 735, and
//     "Generated code and API specifications", line 812.
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
package notificationshttp
