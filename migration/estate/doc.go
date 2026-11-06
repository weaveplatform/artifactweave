// Package estate will be the estate module: Mission Control, MyJFrog,
// JFConnect and bridges.
//
// # Role
//
// estate module, module root. The target layout keeps this module flat: its
// logic will sit in this directory, beside adapters/http, with no api or core
// packages. Only app imports a module root, and other modules import a module
// only through its api package, so app alone reaches estate (decision 0044,
// dependency rules).
//
// # Inputs
//
// None yet. Planned: the Deps that app passes to New. Plan P40 reuses the
// topology identities of P14, which this module reaches through
// federation/api.
//
// # Outputs
//
// None yet. Planned, in this directory (target layout, line 362):
// missioncontrol.go, myjfrog.go, jfconnect.go and bridges.go, with the
// module's constructor.
//
// # References
//
//   - Context map, estate row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns Mission Control, MyJFrog, JFConnect and bridges; phases P40.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 85-104 and 362.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders" and "Composition and HTTP".
//   - Plan:
//     docs/implementation/plan.md#p40-estate-and-hosted-service-integrations.
//
// # Status
//
// Placeholder: implemented by P40.
package estate
