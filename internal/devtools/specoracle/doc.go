// Package specoracle keeps a local, gitignored cache of the OpenAPI blocks
// JFrog embeds on each docs.jfrog.com endpoint page and compares them with the
// specs this project authors under api/. JFrog's definitions are proprietary,
// so the cache is a test oracle only: it is fetched on demand, never committed,
// and the committed artefact is the parity baseline, which lists only paths,
// field names and status codes (decision record 0001).
//
// # Role
//
// devtools layer: the library behind tools/specoracle, whose subcommands the
// Makefile's oracle targets run, and behind TestAccSpecParity in api/; never
// linked into the server binary. It imports only the standard library and
// gopkg.in/yaml.v3.
//
// # Inputs
//
//   - Fetch: the llms.txt index of each API family and the endpoint pages it
//     lists, over HTTP. By default 4 pages at a time, each after a 200 ms
//     pause, with up to 3 attempts on 429, 5xx and transport errors and a
//     backoff that doubles from that pause, under a user agent that names
//     this project.
//   - The cache directory, .specoracle by convention: one
//     <family>/<page>.json file per block, and manifest.json (Load,
//     BuildInventory, Scaffold).
//   - Our bundled specs, api/*.openapi.yaml (LoadOurs), and the accepted
//     gaps, api/parity-baseline.json (ReadBaseline).
//
// # Outputs
//
//   - The cache and its Manifest: each page's file, status and checksum, or
//     why it was skipped or failed.
//   - A Report: the operations JFrog documents that we do not define,
//     divergent operations with their findings, matched operations and our
//     extras, with a per-family Summary and a String that diffs cleanly.
//   - api/parity-baseline.json (WriteBaseline), and the Diff of a report
//     against it: regressions and improvements.
//   - The Inventory of every named schema, field, enum, parameter and
//     operation in the cache, as api/oracle-inventory.json and the Markdown
//     of research 14.
//   - Scaffold: a draft OpenAPI 3.0.3 fragment for one family, every
//     description the TODO(author) Placeholder.
//
// # Invariants
//
//   - The cache is never committed, because JFrog's definitions are
//     proprietary. What is committed from it, the baseline, the inventory
//     and scaffolded fragments, holds facts such as paths, field names,
//     types, enums and status codes, never JFrog's prose.
//   - The comparison covers only what a client observes on the wire, never
//     descriptions, and both sides pass through one normaliser, so a second
//     parser cannot skew it.
//   - One bad page never hides the rest: Fetch records a page that fails or
//     has no block in the manifest, and Load records a block that does not
//     parse in the oracle's Errors.
//   - A missing baseline file reads as an empty baseline, so a repository
//     that forgot to commit it fails on every gap instead of passing.
//   - TestAccSpecParity treats a cache without manifest.json as no cache and
//     skips.
//
// # References
//
//   - Decision 0001:
//     docs/research/decisions/0001-mirror-the-jfrog-api-surface.md.
//   - Research 01: docs/research/01-jfrog-api-landscape.md#parity-oracle;
//     research 14: docs/research/14-data-model-inventory.md.
//   - Plan: docs/implementation/plan.md#p0-foundations, task 10, and
//     #p1-data-model-and-api-schemas, tasks 1 and 3.
//   - docs/testing/ci.md: the parity job, which restores or fetches the
//     cache.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 113 to 116 (the devtools tree) and line 1041 (the mapping row).
//   - https://github.com/weaveplatform/artifactweave/issues/990
//
// # Status
//
// Implemented P0 (fetch, comparison and baseline) and P1 (inventory and
// scaffold). Staged in P9d.
//
// # Testing
//
//   - TestUnitExtractBlock, TestUnitParseIndexAndSlugs: a page's embedded
//     block, an index's links, and the cache's file names.
//   - TestAccFetchWritesCacheAndManifest: against a local HTTP server, pages
//     with a block are cached, a page without one, a 404 and an index that
//     keeps failing are recorded, and a flaky page succeeds on retry.
//     TestAccFetchErrors: no index, no cache directory, a cancelled context.
//   - TestUnitLoadAndCompare, TestUnitLoadOursErrors,
//     TestUnitAddMergesOperationsSharingAKey: both sides load through one
//     normaliser, a broken block becomes a parse error while a spec that
//     cannot be read fails, two pages that document one route merge, and
//     the report, summary and text agree.
//   - TestUnitBaselineRoundTrip: a written baseline reads back, Diff splits
//     regressions from improvements, and an absent file reads as empty.
//   - TestUnitBuildInventory, TestUnitManifestSourcesDegrades,
//     TestUnitScaffold, TestUnitScaffoldHelpers, TestUnitHelpers.
//
// TestAccSpecParity in api/parity_test.go runs the comparison on the real
// specs against the cache and api/parity-baseline.json.
package specoracle
