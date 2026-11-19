// Package notifications is the module root for the webhook catalogue,
// subscription administration and delivery.
//
// # Role
//
// notifications module, module root. Only app imports a module root; other
// modules reach notifications through notifications/api (decision 0044,
// dependency rules). core/webhook supplies presence-aware secret updates and
// safe views over the module's ports and store adapters. This root has no
// constructor or HTTP binding until subscription administration is wired in
// WEB-02 (decision 0044, composition and HTTP).
//
// # Inputs
//
// No module-root inputs yet. core/webhook accepts replacement subscription
// configuration with omitted, empty and replacement secret values kept distinct.
//
// # Outputs
//
// core/webhook returns detached, nonsecret views. adapters/http converts handler
// inputs and generated read variants without activating routes. Planned module
// wiring (target layout, lines 318-321 and 730-734):
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
// Placeholder: implemented by P11. The module root awaits composition in WEB-02;
// core/webhook and adapters/http supply WEB-01 secret handling and conversions.
// WEB-02 owns
// subscription authorization and route binding; P19a owns observed delivery,
// signing, testing and troubleshooting.
package notifications
