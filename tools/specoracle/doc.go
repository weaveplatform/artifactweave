// Command specoracle maintains the gitignored cache of JFrog's embedded
// OpenAPI blocks and reports parity against api/*.openapi.yaml.
//
// # Role
//
// Command, with the repository tooling under tools/: not part of the server
// binary, and it keeps its path through the modular-monolith migration. The
// work is internal/devtools/specoracle; the Makefile's oracle targets run
// its subcommands.
//
// # Inputs
//
// JFrog's published reference pages, on fetch, and the cache they are kept in
// (.specoracle, gitignored: the definitions are JFrog's and are never
// committed); api/*.openapi.yaml for the comparisons. fetch also reads
// api/oracle-inventory.json for the pages each family had, which find the
// family in its parent index when its own index fails (#1308). baseline and
// inventory refuse a cache that lacks a family, and report warns of it. The
// subcommands:
//
//	specoracle fetch    [-cache .specoracle]            download the documented blocks
//	specoracle report   [-cache .specoracle] [-api api] print the comparison
//	specoracle baseline [-cache .specoracle] [-api api] -update
//	                                                    rewrite api/parity-baseline.json
//	specoracle inventory [-cache .specoracle] [-api api] [-out docs/...md]
//	                                                    write the fact inventory (JSON + markdown)
//	specoracle scaffold -family <slug> -service <svc> -out <fragment.yaml>
//	                                                    draft a spec fragment from one family
//
// # Outputs
//
//   - The cache, the parity report, api/parity-baseline.json,
//     api/oracle-inventory.json with its Markdown inventory, and draft spec
//     fragments with TODO(author) descriptions.
//
// # References
//
//   - Decision 0001:
//     docs/research/decisions/0001-mirror-the-jfrog-api-surface.md.
//   - Research 01: docs/research/01-jfrog-api-landscape.md; research 14:
//     docs/research/14-data-model-inventory.md.
//   - Plan: docs/implementation/plan.md#p0-foundations, task 10, and
//     #p1-data-model-and-api-schemas, tasks 1 and 3.
//
// # Status
//
// Implemented P0 (fetch, report and baseline), P1 (inventory and scaffold)
// and P5 (the Builds API family).
package main
