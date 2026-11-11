// Package openapibundle merges the per-family spec fragments under
// api/src/<service>/ into the one document oapi-codegen and the parity test
// read. Authors edit fragments so several families can be written in
// parallel without one enormous file; the bundle is generated and committed
// so a reader of api/<service>.openapi.yaml sees the whole surface.
//
// # Role
//
// devtools layer: the library behind tools/openapi-bundle (make bundle, which
// make generate runs before it generates code); never linked into the server
// binary. It imports only the standard library and gopkg.in/yaml.v3.
//
// # Inputs
//
//   - A service's fragment directory, api/src/<service>/: info.yaml, the
//     document head (openapi, info, servers, tags, security and
//     externalDocs, nothing else), and the paths-*.yaml and
//     components-*.yaml fragments, which hold only paths and components.
//     Other files in the directory are not read.
//   - For BundleAll, the root that holds one such directory per service, so
//     adding a service is adding a directory.
//
// # Outputs
//
//   - Bundle: the merged document as YAML, headed by a comment that says it
//     is generated from the fragments and is not to be edited.
//   - BundleAll: api/<service>.openapi.yaml for each service directory, and
//     the services written, sorted.
//   - An error for a missing info.yaml, a key that info.yaml or a fragment
//     may not hold, a fragment that does not parse or is not a mapping, and
//     a path, operationId or component that a second fragment declares
//     again.
//
// # Invariants
//
//   - The bundles are generated and committed: make generate rewrites them,
//     and make verify-generate fails when a committed bundle differs from
//     what its fragments give (decision 0002).
//   - The output does not depend on which fragment declared what: fragments
//     are read in name order, and paths and each components section are
//     sorted by key, keeping each value's comments and scalar styles.
//   - A second declaration of a path, operationId or component fails the
//     bundle rather than replacing the first, so two families cannot
//     silently claim the same route or schema.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - docs/implementation/api-description-conventions.md.
//   - Plan: docs/implementation/plan.md#p1-data-model-and-api-schemas, task
//     3, and #p1b-enterprise-products-specs-and-model, task 1.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 113 to 116 (the devtools tree) and line 1009 (the mapping row).
//   - https://github.com/weaveplatform/artifactweave/issues/990
//
// # Status
//
// Implemented P1 (the fragment bundler) and P1b (any number of services).
// Moved here in P9d.
//
// # Testing
//
//   - TestUnitBundleMergesAndSorts: the head comes from info.yaml, paths and
//     schemas are sorted across fragments, and files that are not fragments
//     are left out.
//   - TestUnitBundleErrors: one case per error above; an empty fragment
//     bundles.
//   - TestUnitBundleAll: every service directory is bundled and listed; a
//     missing source, an unwritable output and a service without info.yaml
//     fail.
//
// make verify-generate checks the committed bundles against their fragments.
package openapibundle
