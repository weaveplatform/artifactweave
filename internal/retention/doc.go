// Package retention will be the retention module: cleanup policies, archive
// policies, the cold tier and restore jobs.
//
// # Role
//
// retention module, module root. Only app imports a module root; other
// modules reach retention through retention/api (decision 0044, dependency
// rules). Its storage skeleton (the module's ports, models and store
// adapters) sits in subpackages ahead of its phase; skeleton modules get no
// constructor until then (decision 0044, composition and HTTP).
//
// # Inputs
//
// None yet. Planned: the Deps that app passes to New. Cleanup and archive
// runs are scheduled (decision 0010), and archiving moves items to the cold
// tier (decision 0024).
//
// # Outputs
//
// None yet. Planned: module.go with New(Deps). The module's layers are its
// subpackages: api (P11, P16), core/cleanup (P11), core/archive and
// core/restore (P16), and adapters/http (P11, P16).
//
// # References
//
//   - Context map, retention row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns cleanup policies, archive policies, the cold tier and restore
//     jobs; phases P11 and P16.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 85-104 and 325-329.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders" and "Composition and HTTP".
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md.
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication,
//     #p16-cold-storage-archive-and-retention.
//
// # Status
//
// Placeholder: implemented by P11, P16.
package retention
