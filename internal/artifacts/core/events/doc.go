// Package events captures the artifacts module's typed durable transition
// facts without binding them to a vendor webhook representation.
//
// # Role
//
// Module-owned shared support for item, registry and build mutations. Topics
// and payloads are published by artifacts/api; platform owns only their opaque
// durable storage and transaction finalization.
//
// # Inputs
//
// The owning mutation's context, typed immutable facts, time, public instance
// identity and a secret-free repository context projection.
//
// # Outputs
//
// Sanitized outcome/actor metadata, descriptive repository/item snapshots,
// bounded unambiguous entity keys and transaction-staged events.
//
// # Invariants
//
// Caller credentials and full repository configurations never enter payloads.
// Repository context is descriptive, not an authorization or lifetime fence;
// it adds no late row lock. Facts are encoded during business work, and final
// callbacks only append them. Append failure rolls back the business unit.
//
// # References
//
// docs/research/decisions/0011-webhooks.md, 0033-persistent-work-queue.md and
// 0044-modular-monolith-migration.md; implementation ticket #1750.
//
// # Status
//
// P11 EVT-02 producer support. Vendor envelopes/signatures remain in P19a.
//
// # Testing
//
// Owning-service tests assert committed topics/facts and append-failure
// rollback; platform contracts prove finalization and stable source identity.
package events
