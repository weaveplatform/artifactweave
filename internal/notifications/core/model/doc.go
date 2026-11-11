// Package model holds the notifications module's entity values: webhook
// subscriptions with their criteria and handlers, the delivery records the
// troubleshooting view reads, and the event types each webhook domain
// defines.
//
// # Role
//
// notifications module, core layer, entity values. The module's store port
// takes and returns them, and P11's webhook catalogue, criteria, signing and
// delivery will build on them. The package imports only the standard
// library.
//
// # Inputs
//
//   - For ValidateEventTypes, a webhook domain and the event types a
//     subscription names.
//
// # Outputs
//
//   - WebhookSubscription, with WebhookCriteria, WebhookHandler, Header and
//     NamedSecret; and WebhookDelivery.
//   - WebhookDomain with its fifteen Domain* constants, and HandlerType with
//     HandlerWebhook and HandlerCustomWebhook.
//   - ValidateEventTypes and its sentinel ErrInvalidEventType.
//
// # Invariants
//
//   - Event types are JFrog's names, which JFrog's Terraform provider
//     validates client-side and expects back verbatim. The AppTrust and
//     Curation domains document none, so any non-empty name is kept as
//     given.
//   - Handler secrets and named secret values have no JSON name: they are
//     sealed in a table of their own (decision 0017) and never written into
//     the subscription's document.
//
// # References
//
//   - Decision 0011: docs/research/decisions/0011-webhooks.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md
//     (the sealed handler secrets).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence").
//   - Research 07: docs/research/07-webhooks-retention-projects.md (the
//     domains and their event types). Research 15:
//     docs/research/15-data-model.md, lines 684 to 686 (the webhook tables)
//     and line 767 (their columns as built).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 100 to 103 (a storage skeleton ahead of its phase), lines 317 to
//     323 (the notifications module), lines 577 and 578 (each module owns
//     its store ports) and line 1058 (the shared types split into each
//     module's core/model).
//   - Plan: docs/implementation/plan.md#p2-stores, task 0,
//     #p11-projects-webhooks-retention-replication and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1053
//
// # Status
//
// Implemented P2: the webhook values among the shared domain types (pull
// request 3). Moved here in P9d. The behaviour that uses them arrives with
// P11.
//
// # Testing
//
//   - webhook_test.go: TestUnitWebhookEventTypes (known event types of a
//     listed domain and any name in an unlisted one; an unknown domain, no
//     event types, a name the domain does not define and an empty name fail
//     with ErrInvalidEventType; a handler's secrets stay out of its JSON).
//
// The metadata store's Webhooks contract subtest stores and reads these
// values on its in-memory, SQLite and PostgreSQL stores.
package model
