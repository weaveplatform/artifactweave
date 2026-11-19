// Package notificationshttp converts webhook handler requests and safe views
// for the Event API; subscription route binding arrives in WEB-02.
//
// # Role
//
// notifications module, adapter layer (HTTP). The package is named
// notificationshttp so that it does not shadow net/http.
//
// # Inputs
//
// Generated Event API handler unions, whose secret fields retain omission
// separately from explicit empty values, and nonsecret core/webhook views.
//
// # Outputs
//
// Presence-aware handler inputs and explicit generated read variants: plain
// secrets are empty with secret_configured, and custom secrets expose names
// only. These helpers activate no HTTP operation by themselves.
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
// P11 WEB-01 supplies safe handler conversions. WEB-02 binds subscription
// administration; P19a owns observed delivery/test/troubleshooting behavior.
//
// # Testing
//
// handlers_test.go checks real generated response JSON, both handler types,
// omitted/empty/replaced values and errors that do not echo private input.
package notificationshttp
