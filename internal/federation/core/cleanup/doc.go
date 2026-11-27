// Package cleanup coordinates durable member exclusion and bounded signed
// download evidence for federated retention. Artifacts owns actual deletion.
//
// # Role
//
// Federation module, core layer. Artifacts owns the existing package index;
// retention owns policy criteria, current caller authority and run/lease state.
//
// # Inputs
//
// Current secret-free repository lifetime and membership, current peer grants,
// bounded version identities and request-bound signed peer answers. Network
// collection has no surrounding metadata transaction. Artifacts supplies durable
// hosted admission/barrier records; retention supplies per-file run ownership.
//
// # Outputs
//
// Private bounded evidence, immutable per-file intents, signed participant
// preparation/outcome/release, and monotonic per-member release acknowledgements.
// Missing observations or unknown admissions/outcomes retain content. Preparation
// drains existing hosted responses and closes new admission in each repository;
// freshness alone never grants destructive exclusion. Publication binds ordinary
// artifact effects to an irreversible outcome and final replication watermark.
// Release requires abort or the participant's applied committed frontier; one
// unavailable peer does not prevent other independently safe releases.
//
// # References
//
//   - docs/research/decisions/0010-retention-trash-and-gc.md.
//   - docs/research/decisions/0018-package-index.md.
//   - docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - docs/research/decisions/0022-federated-repositories.md.
//   - https://github.com/weaveplatform/artifactweave/issues/1798.
//
// # Status
//
// FED-07 coordinated exclusion implementation. Retention invokes ordinary
// artifact deletion under original current authority and run/worker fences only
// after every participant has acknowledged a durable barrier.
//
// # Testing
//
// Isolated observation tests cover bounds and local fences. Signed peer-server
// acceptance tests exercise preparation, unknown readers, rollback, terminal
// replay, partial settlement and repository replacement on in-memory and SQL
// stores. Process-level native scenarios exercise ordinary federated deletion.
package cleanup
