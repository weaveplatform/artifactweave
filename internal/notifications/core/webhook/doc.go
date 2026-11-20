// Package webhook manages guarded subscription configuration and safe read views.
//
// # Role
//
// Notifications core service over core/port. It separates presence-aware secret
// writes from the complete models stored by the sealed SQL adapter. HTTP
// adapters convert their generated request models explicitly and supply current
// authorization and subscription/criteria validation in their enclosing unit of
// work; this package activates no route or delivery worker.
//
// # Inputs
//
// Input contains replacement subscription settings and handler configuration.
// A nil secret value keeps an existing value, a nonempty value replaces it and
// an explicitly empty value wipes it. A custom handler's named-secret list is
// a replacement set: omitted names are removed, including an empty list.
//
// # Outputs
//
// Service Create, Update, Get and List return View values whose handler types
// contain no private secret value. Views are inputs to the HTTP adapter's wire
// conversions; they are not the privileged storage model. The whole-model
// WebhookStore remains available internally to later delivery execution.
//
// # Invariants
//
// Secret merge and whole-model replacement share the caller's transaction.
// A handler-type change never inherits the old type's secrets. A listed custom
// secret keeps its value only by the same name in the same handler type.
// Errors never include secret values. The SQL adapter retains its existing
// sealing purpose and handler-index/name identity.
//
// # References
//
//   - docs/research/decisions/0011-webhooks.md.
//   - docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - docs/research/decisions/0044-modular-monolith-migration.md.
//   - api/src/event/paths-webhooks.yaml.
//   - https://github.com/weaveplatform/artifactweave/issues/1752.
//
// # Status
//
// P11 WEB-01 supplies secret handling for WEB-02 subscription administration.
// WEB-02 adds transactional authorization and criteria/template validation;
// observed delivery/signing is P19a.
package webhook
