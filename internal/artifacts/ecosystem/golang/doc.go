// Package golang is the format of Go module repositories (decision 0035).
// It reads the GOPROXY paths a request names, checks the path of every
// version file a client deploys, chooses what @v/list and @latest answer,
// and keeps the package index. In the index a module is a package, and
// each of its versions links whichever of the version's .zip, .mod and
// .info are stored.
//
// # Role
//
// artifacts module, ecosystem layer: the format rules of the Go modules
// slice (package type go), in a directory named golang because go is a Go
// keyword. Its Handler plugs into the item service through the format
// hooks (decision 0032), so every write and delete in a Go repository
// reaches it whichever API carried it. The slice's protocol package,
// golang/http, answers HTTP with the paths, versions and index kept here.
// Its upstream rules classify and validate what a remote Go repository
// fetches over core remote's shared retrieval and cache policy (decision
// 0007), which this package imports and which never imports it back
// (decision 0044).
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for Go repositories to Handler; the protocol
//     package (golang/http), which parses request paths (ParseRequest,
//     ParseFile), checks a .info (CheckInfo) and chooses versions (Listed,
//     Latest); and the P10 virtual resolver, which will read remote Go
//     repositories through GoSource (no caller yet).
//   - Driven: the format hooks' environment (format.Env), through which
//     Handler reads items and their properties and keeps the package index;
//     and the application's authority-fenced source service
//     (artifacts/core/remote/source), through which GoSource reads a remote
//     repository over the shared remote cache policy, never a raw transport.
//
// # Outputs
//
//   - Request paths and version files: Request, File, Kind with its
//     operations, the version file extensions, ErrNoOperation and
//     InvalidPathError.
//   - Version selection for @v/list and @latest (Listed, Latest, ListTime),
//     and the .info checks (CheckInfo, MaxInfo).
//   - Handler: the hooks that admit, index, classify and reindex Go
//     version files, and the refusal of different content over a stored
//     version file (Republished).
//   - The Go upstream rules of remote repositories
//     (docs/implementation/go-upstream-source.md): GoSource, the GOPROXY
//     reader of a remote Go repository, and GoResource, the trusted
//     resource for a GOPROXY path, whose Validate refuses an incomplete or
//     invalid body before it is cached (ErrGoResponse).
//
// # Invariants
//
//   - A version file is stored at <module>/@v/<version>.<ext>, with the
//     module path and version as written, not case-encoded, because that is
//     what jf go-publish deploys and records in its build-info.
//   - Different content over a stored .zip or .mod is refused, whichever
//     API carries it, because go.sum pins it (Republished); replacing one
//     takes a delete first.
//   - Error texts reach the go command and the JFrog CLI, so they stay
//     byte-identical.
//
// # References
//
//   - Decision 0035: docs/research/decisions/0035-go-modules.md.
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format hooks and optional interfaces).
//   - docs/implementation/go-upstream-source.md (the Go upstream rules of
//     remote repositories) and
//     https://github.com/weaveplatform/artifactweave/issues/633, which added
//     them.
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (format rules classify and validate; core remote owns transport and
//     cache policy).
//   - Research 26: docs/research/26-go-modules.md.
//   - JFrog Go registry documentation, as decision 0035 cites it:
//     https://docs.jfrog.com/artifactory/docs/go-modules.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 230 (ecosystem/golang), line 380 (why golang), line 654 (the
//     upstream rules) and lines 1002 and 1014 (the mapping rows).
//   - Plan: docs/implementation/plan.md#p7-npm-go-maven-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/1014 and
//     https://github.com/weaveplatform/artifactweave/issues/1015
//
// # Status
//
// Implemented P7, with remote retrieval in P9. Moved here in P9d. Roadmap:
// virtual.go (P10), the Merger that merges the Go module version list
// (@v/list) for virtual repositories.
//
// # Testing
//
//   - path_test.go: request paths, case decoding and refusals.
//   - version_test.go and info_test.go: version order, @v/list and
//     @latest, and the .info checks.
//   - handler_test.go and cache_test.go: registration, admission,
//     republishing, the package index, .info times, classification, store
//     failures, and a remote repository's cache indexed alone.
//   - contract_test.go: the format contract suite (artifacts/core/formattest).
//   - remote_test.go and remote_fault_test.go: GoSource through the real
//     source service and remote cache policy against a fake upstream
//     (escaped identities, exact bytes, metadata refresh, immutable files,
//     negative caching, offline reads, credentials, repository isolation,
//     delivery fences, invalid and interrupted bodies) and the body
//     validation; remote_fixture_test.go holds the policy fixture they
//     share with nothing else.
package golang
