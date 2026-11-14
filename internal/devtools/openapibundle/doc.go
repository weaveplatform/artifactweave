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
//   - SharedFile, api/src/components-shared.yaml, beside the service
//     directories: components only, which every service's bundle merges
//     (decision 0043, "Where the declarations live"). It is a file, not a
//     directory, so BundleAll never bundles it as a service.
//   - Each operation's x-cost, which decides whether admission sees it.
//
// # Outputs
//
//   - Bundle: the merged document as YAML, headed by a comment that says it
//     is generated from the fragments and is not to be edited. It holds the
//     shared components, and every operation whose x-cost is not exempt
//     (one without x-cost included) answers '429' with the shared
//     TooManyRequests and '503' with the shared ServiceUnavailable unless it
//     declares that status itself.
//   - BundleAll: api/<service>.openapi.yaml for each service directory, and
//     the services written, sorted.
//   - An error for a missing info.yaml, a key that info.yaml or a fragment
//     may not hold, a fragment that does not parse or is not a mapping, and
//     a path, operationId or component that a second fragment declares
//     again, the shared fragment's included, and a shared fragment that
//     holds anything but components.
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
//   - An operation's own 429 or 503 is never replaced: it is declared
//     because admission answers its path in another envelope, or because
//     the status also reports a condition of the operation's own. No
//     header, IdempotencyKey or LinkNext is added to any operation; one
//     references them itself once the server honours them. Nothing is
//     injected that the shared fragment does not declare.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md.
//   - Decision 0043, "Where the declarations live":
//     docs/research/decisions/0043-api-reliability.md.
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
// Moved here in P9d. The shared fragment and the admission statuses: #1405.
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
//   - TestUnitBundleSharedAndAdmission: the shared components are merged;
//     an ordinary and an unclassified operation gain the 429 and 503 in
//     status order; an exempt probe and an operation's own 429 and 503 are
//     left alone.
//   - TestUnitBundleSharedPartial and TestUnitBundleWithoutShared: only the
//     shared responses that exist are injected; without the fragment
//     nothing is.
//   - TestUnitBundleSharedErrors: a shared fragment with paths, one that
//     does not parse or is not a mapping, and a service that redeclares a
//     shared component fail.
//
// make verify-generate checks the committed bundles against their fragments.
package openapibundle
