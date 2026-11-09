// Package machinelearninghttp implements the FrogML SDK's Artifactory model
// transaction and manifest API on local and one-node federated repositories.
// The routes sit on Artifactory's /artifactory/api/machinelearning/{repo}
// prefix; the layout and the publication rules are the format package's
// (artifacts/ecosystem/machinelearning), and this package answers HTTP with
// them.
//
// # Role
//
// artifacts module, ecosystem layer: the native FrogML model transaction and
// manifest protocol of the Machine Learning slice. It is named
// machinelearninghttp so that it does not shadow net/http. Outside tests,
// only its own http subtree, the module's http adapters and app may import
// it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type machinelearning.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, stages content and stores items
//     through the item service, where the format's hooks admit them, reads
//     items, properties and blobs, runs a commit in one metadata transaction
//     and writes errors; the request's base URL (platform/httpkit/baseurl),
//     onto which download URLs point; the caller's principal (the kernel),
//     who owns a transaction; and the format package, which checks
//     identities, decodes, reads and verifies manifests, rechecks a
//     transaction's owner and expiry and rebuilds the index.
//   - Settings: none.
//
// # Outputs
//
//   - Protocol: the routes and the Artifactory error profile.
//   - Answers: a started transaction's ID and its files, lead,
//     dependencies and code upload paths (POST
//     model/{name}/start-transaction[/{version}], 200); a committed
//     version's ID and version (PUT
//     model/entity-manifest/{name}/{version}/{transaction}, 201); and a
//     published manifest with a download URL for every file (GET
//     model/entity-manifest/{name}/{version}, 200, not cached). Each route
//     also takes a {namespace}/{name} model.
//
// # Invariants
//
//   - Identities follow the format package's rules after one unescape, and
//     a request with a query is refused (400). A start without a version
//     gets the current Unix millisecond time as its version.
//   - Starting needs an authenticated caller (401) with the write grant on
//     the version's folder, and a version not yet published (409). The
//     transaction ID is the current Unix millisecond time, tried up to 32
//     times while it collides with a stored transaction (409 after that).
//   - A commit is multipart form data (415) of at most the manifest limit
//     plus 64 KiB, with one modelManifest field and at most one
//     additionalData field of at most 64 KiB, 256 properties, 255-byte keys
//     and 4,096-byte values (400). The manifest must name the route's model
//     and version (400), and its download URLs are dropped before it is
//     stored.
//   - A commit publishes in one metadata transaction or not at all: it
//     rechecks the transaction's owner (403) and expiry (409), the write
//     grant on every declared file and on the manifest (403), and every
//     staged file's size and SHA-256 (409), then stores the files, the
//     manifest with its properties and the index. Repeating the same
//     manifest and properties succeeds; anything else for a published
//     version conflicts (409).
//   - The manifest GET needs the read grant on the manifest and on every
//     declared file (403), and answers only a complete version (404 for a
//     missing file, 409 for one that differs).
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit, and "P9c Machine Learning adapter (#780)"); decision
//     0007: docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, which this protocol does not serve: a remote answers
//     501, and configuration refuses one for this type).
//   - docs/operations/machinelearning.md (the FrogML recipe, the request
//     sequence, the transaction lifetime, the limits and the evidence) and
//     docs/client-recipes/machinelearning.md (the client recipe).
//   - JFrog Machine Learning repositories:
//     https://docs.jfrog.com/artifactory/docs/machine-learning-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 235 (ecosystem/machinelearning) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p32-data-and-model-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/780 and
//     https://github.com/weaveplatform/artifactweave/issues/1052
//
// # Status
//
// Implemented P9c, which delivered the plan's P32 group (issues #777 to
// #780 and #789). Staged in P9d.
//
// # Testing
//
//   - protocol_test.go (external, machinelearninghttp_test, importing the
//     internal/server root): the publication lifecycle and transaction
//     validation.
//   - fault_test.go (external, importing the internal/server root): a
//     commit rolled back on storage failure, and start and replay storage
//     failures.
//   - authority_test.go (external): model file permissions and transaction
//     ownership, a commit that rechecks every destination, and model
//     identity with immutable commits.
//   - privacy_test.go (external): private model records that cannot escape,
//     and concurrent commits that publish one version.
//   - parse_test.go (in-package): commit parsing, and identities with
//     invalid escapes refused.
//
// The @machinelearning acceptance scenarios publish and download through
// the pinned FrogML SDK on local and federated repositories, and refuse
// invalid credentials.
package machinelearninghttp
