// Package notifications will be the notifications module: the webhook
// catalogue, criteria, signing and delivery.
//
// # Role
//
// notifications module, module root. Only app imports a module root; other
// modules reach notifications through notifications/api (decision 0044,
// dependency rules). The target layout keeps this module small and flat, so
// its logic will sit in this directory beside the constructor. Its storage
// skeleton (the module's ports, models and store adapters) sits in
// subpackages ahead of its phase; skeleton modules get no constructor until
// then (decision 0044, composition and HTTP).
//
// # Inputs
//
// None yet. Planned: one subscription per catalogue topic on the event bus in
// platform/events, and the Deps that app passes to New (target layout, lines
// 730-734).
//
// # Outputs
//
// None yet. Planned, in this directory (target layout, lines 318-321 and
// 730-734):
//
//   - module.go: New(Deps), which app calls in module order.
//   - catalogue.go: JFrog event names mapped to module events.
//   - criteria.go, delivery.go, signing.go, retry.go and troubleshoot.go:
//     subscription criteria, delivery with retries and its log, request
//     signing, and the troubleshooting view.
//   - subscribe.go: one bus subscription per catalogue topic.
//
// # References
//
//   - Context map, notifications row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns the webhook catalogue, criteria, signing and delivery; phases P11.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 85-104 and 317-323, and "Webhooks and events (P11, P13)",
//     lines 717-744.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders" and "Composition and HTTP".
//   - Decision 0011: docs/research/decisions/0011-webhooks.md.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication.
//
// # Status
//
// Placeholder: implemented by P11.
package notifications
