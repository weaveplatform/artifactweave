// Package login serves the optional browser/native-client approval endpoints.
//
// # Role
//
// access module, adapter layer (HTTP), below access/adapters/http.
//
// # Inputs
//
// Exact-origin form submissions, session-bound CSRF, npm start/poll requests,
// Terraform authorization/code exchange and the access/api NativeLogin port.
//
// # Outputs
//
// Escaped server-rendered HTML without JavaScript, protected cookies,
// no-store JSON and a code-only localhost redirect. No general console.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 295.
//   - Plan:
//     docs/implementation/plan.md#p21-browser-and-enterprise-authentication.
//   - Context map: the access row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0019:
//     docs/research/decisions/0019-human-authentication-tiers.md.
//   - https://github.com/weaveplatform/artifactweave/issues/262
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Implemented for #262: optional server-rendered npm/Terraform approval.
// External identity providers and the full console remain separate work.
package login
