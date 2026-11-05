// Command clientrecipes validates and renders the reusable client connection
// recipes, retaining fingerprints of the tests and version pins behind them.
//
// # Role
//
// Command, with the repository tooling under tools/: not part of the server
// binary, and it keeps its path through the modular-monolith migration. make
// client-recipes runs it, and the quality gate runs it with -check.
//
// # Inputs
//
//   - docs/implementation/package-format-inventory.json, the package-format
//     catalog.
//   - docs/client-recipes/src/<type>.json, one recipe per package type, each
//     naming the client, its configuration, publish and install steps and the
//     evidence behind them.
//   - The evidence files each recipe names, such as workflows and client
//     images.
//
// # Outputs
//
//   - docs/client-recipes/index.md and one <type>.md per recipe.
//   - docs/client-recipes/evidence-lock.json: the SHA-256 of every evidence
//     file, so a change to one shows in the diff of the recipes that cite it.
//   - With -check, a failure instead of a write when a committed file
//     differs.
//
// # References
//
//   - docs/client-recipes/index.md.
//   - docs/testing/ci.md.
//   - https://github.com/weaveplatform/artifactweave/issues/791
//
// # Status
//
// Implemented P9c (issue 791).
package main
