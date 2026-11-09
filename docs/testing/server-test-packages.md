# Server route test packages

Server-assembly tests exercise the production router with the protocol routes,
authentication, repositories, and storage wired together. Each ecosystem's
suites live in a separate Go package under `test/server/artifacts/<type>`, and
each module's suites under `test/server/<module>`. This makes the test package,
rather than a test-name regular expression, the unit that CI can select.
A package builds the router through `test/server/support/harness`, with
`harness.Router` and a `harness.Config`, and does not import `internal/server`
itself; fixtures used by that package live with it.

`test/server/support` owns reproducible authentication seeds, request helpers,
NuGet package bytes, npm tarballs and Artifactory error decoding used by more
than one suite. It also holds shared OCI manifest and digest fixtures plus the
Docker route inventory. It imports no server package, so both `internal/server` tests and independent
server-assembly packages can use it without an import cycle. Production code
must not import it. Server configuration and storage setup stay with each suite
because their repository and permission models differ.

`test/server/support/harness` is the one place the suites build the router
from. `Config` and `Tune` alias the router's configuration and its option
function, `Mirrors`, `MirrorStatus`, `SearchLimits` and `DefaultRedirectTTL`
alias the configuration types and the default that suites name, and `Router`
builds the production handler, so a change of router edits the harness and no
suite. The harness also exports the item world of the
root `internal/server` tests (`NewItemWorld`, with `Do`, `Item` and
`WantStatus`) for the route suites that move out of that package, and
`RepoFile`, which resolves a root-relative fixture path. Because the harness
imports `internal/server`, `test/server/support` must never import it, and the
root tests keep their in-package `newItemWorld`, which must build the same
world.

Two fixtures under `test/server/support` build a world that more than one
suite shares. `test/server/support/registryfixture` assembles the full server
with the registry's authentication, repository, and signed blob setup for
Docker and Helm OCI; `test/server/support/agentcommon` assembles the world of
Agent Packages and Agent Plugins. Like the harness, they build the router, so
`test/server/support` never imports them.

`test/server/access` is the access module's suite: authentication, tokens,
users and groups, permissions, roles and environments, key pairs and
certificates, the OIDC exchange and API restrictions, through the full router.
`test/server/configuration` is the configuration module's suite: the base URL
behind a chain of proxies and the configured base URL. `test/server/artifacts`
is the artifacts module's management API suite: items with their checksums,
properties and sidecars, the storage views and summary, the trash can, copy and
move, build-info and promotion, the GET searches and AQL, reindex, repository
configuration, remote sources and upstream searches, download redirects,
multipart uploads and idempotent build appends. Each suite's `fixture_test.go`
keeps the root helper names the tests call (`statusFixture`, `newItemWorld`,
`newRepoWorld`, `tokenFixture`, `do`, `basic`) as adapters over the harness and
`test/server/support`.
`test/server/artifacts/nuget` contains the NuGet v2/v3 status table, route ×
principal matrix, API-key and force-authentication cases, symbols lifecycle,
remote status cases, and generated reindex method check.
`test/server/artifacts/npm` contains the npm status table, route × principal
matrix, authentication, reindex and strict-layout migration assertions.
`test/server/artifacts/golang` contains the Go module status table and route ×
principal matrix, including publish and permission checks.
`test/server/artifacts/maven` contains Maven status and principal matrices,
metadata, snapshot, REST and version-resolution cases.
`test/server/artifacts/pypi` contains PyPI upload, simple index, JSON metadata,
reindex, status and principal-matrix assertions. `test/server/artifacts/helm`
contains classic Helm index, chart, provenance, layout, status and principal
assertions. `test/server/artifacts/docker` contains Docker/OCI blob, manifest,
tag, registry token, OIDC, reverse proxy, status and principal-matrix
assertions. `test/server/artifacts/helmoci` preserves the Helm OCI registry
push, AQL, properties and build-info sequence from `jf helm push`.
`test/server/artifacts/terraform` owns the module and provider registry status
and principal matrices, signed downloads, network mirror, and real
Terraform/JF publish and install routes.
`test/server/artifacts/terraformbackend` owns backend workspace, state,
locking, encryption, item-route, status and principal assertions.
`test/server/artifacts/agentpackages` owns APM package publish, discovery,
search, permissions, lifecycle and reindex through a shared agent format router
fixture. `test/server/artifacts/agentplugins` owns plugin marketplace,
permission, lifecycle and reindex routes using the same fixture.
`test/server/artifacts/gems` owns RubyGems and Bundler push, permissions,
metadata, yanks, token authentication, remote caching, virtual members, and
indexes that fail closed when a stored archive is missing or corrupt; it runs
the gem and Bundler clients when they are installed. Run each independently
with:

```sh
go test ./test/server/access
go test ./test/server/configuration
go test ./test/server/artifacts
go test ./test/server/artifacts/nuget
go test ./test/server/artifacts/npm
go test ./test/server/artifacts/golang
go test ./test/server/artifacts/maven
go test ./test/server/artifacts/pypi
go test ./test/server/artifacts/helm
go test ./test/server/artifacts/docker
go test ./test/server/artifacts/helmoci
go test ./test/server/artifacts/terraform
go test ./test/server/artifacts/terraformbackend
go test ./test/server/artifacts/agentpackages
go test ./test/server/artifacts/agentplugins
go test ./test/server/artifacts/gems
```

`go test ./test/server/artifacts/...` runs the management API suite and every
ecosystem suite at once.

The root `internal/server` package still contains shared cross-format and
private server helper tests. NuGet-only, npm-only, Go-only, Maven-only, PyPI-only,
classic Helm-only, Docker-only, Helm OCI-only, Terraform registry-only,
Terraform backend-only, Agent Packages-only and Agent Plugins-only route
assertions no longer run from it, nor do the router-level access,
configuration and artifacts management API assertions. The root registration and stub guards retain
the implemented route patterns through
`test/server/support.NuGetRoutes`, `NPMRoutes`, `GoRoutes`, `MavenRoutes`,
`PyPIRoutes`, `HelmRoutes`, `DockerRoutes`, `TerraformRoutes` and
`TerraformBackendRoutes`; those format packages check that every pattern has
both a status and principal row. The shared P8 reindex
envelope and generic v1 `encryptStates` repository read remain in root because
they cover several formats.
Its `statusFixture`, `matrixFixture`, and `newItemWorld` construct different
repository matrices and remain for the shared root assertions. The in-package
fixtures that several root files share live in `fixtures_test.go`, and a suite
split out of the package keeps its in-package part, which calls unexported
server code, in `<name>_internal_test.go`. Their common auth, request, and package
primitives now come from `test/server/support`. Keep a root wrapper where a
shared helper has many existing callers; the wrapper must delegate rather than
maintain another copy of fixture behavior.
An impact planner must run the root package whenever a change could affect a
shared assertion. NuGet-only, npm-only,
Go-only, Maven-only, PyPI-only, classic Helm-only, Docker-only, Helm OCI-only,
Terraform registry-only, Terraform backend-only, Agent Packages-only or Agent
Plugins-only changes can select their respective `test/server/artifacts/<type>`
package plus any recorded root dependency once #673 activates the impact map.
Shared server, authentication, routing, store, and registration changes require
all server packages. A full qualification runs `go test ./...` and retains the
original coverage floors.

The [server test ownership inventory](server-test-ownership.md) records every
remaining root test file, each server test package's format owner, and the
shared root tests that use format-specific fixtures. #673 must consume these
boundaries when activating selective CI.

When moving another suite, preserve its assertions and server assembly path,
move or extract its fixtures with it, and demonstrate that `go test` can run the
new package independently. Then update the CI test selection map and its dependency
tests before allowing CI to omit any root package work.
