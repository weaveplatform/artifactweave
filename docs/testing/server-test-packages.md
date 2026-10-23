# Server route test packages

Server-assembly tests exercise `internal/server.New` with the protocol routes,
authentication, repositories, and storage wired together. Protocol suites should
live in separate Go packages under `test/server/<family>`. This makes the test
package, rather than a test-name regular expression, the unit that CI can select.
A package may import `internal/server` and its public configuration surface;
fixtures used by that package live with it.

`test/server/support` owns reproducible authentication seeds, request helpers,
NuGet package bytes, npm tarballs and Artifactory error decoding used by more
than one suite. It also holds shared OCI manifest and digest fixtures plus the
Docker route inventory. It imports no server package, so both `internal/server` tests and independent
server-assembly packages can use it without an import cycle. Production code
must not import it. Server configuration and storage setup stay with each suite
because their repository and permission models differ.

`test/server/registryfixture` assembles the full server with the registry's
authentication, repository, and signed blob setup for Docker and Helm OCI.

`test/server/shared` contains the full-router authentication assertion.
`test/server/nuget` contains the NuGet v2/v3 status table, route × principal
matrix, API-key and force-authentication cases, symbols lifecycle, remote
status cases, and generated reindex method check. `test/server/npm` contains
the npm status table, route × principal matrix, authentication, reindex and
strict-layout migration assertions. `test/server/golang` contains the Go module
status table and route × principal matrix, including publish and permission
checks. `test/server/maven` contains Maven status and principal matrices,
metadata, snapshot, REST and version-resolution cases. `test/server/pypi`
contains PyPI upload, simple index, JSON metadata, reindex, status and
principal-matrix assertions. `test/server/helm` contains classic Helm index,
chart, provenance, layout, status and principal assertions. `test/server/docker`
contains Docker/OCI blob, manifest, tag, registry token, OIDC, reverse proxy,
status and principal-matrix assertions. `test/server/helmoci` preserves the
Helm OCI registry push, AQL, properties and build-info sequence from
`jf helm push`. Run each independently with:

```sh
go test ./test/server/shared
go test ./test/server/nuget
go test ./test/server/npm
go test ./test/server/golang
go test ./test/server/maven
go test ./test/server/pypi
go test ./test/server/helm
go test ./test/server/docker
go test ./test/server/helmoci
```

The root `internal/server` package still contains cross-format tests and other
protocol-specific suites. NuGet-only, npm-only, Go-only, Maven-only, PyPI-only,
classic Helm-only, Docker-only and Helm OCI-only route assertions no
longer run from it. The root registration and stub guards retain the implemented route patterns through
`test/server/support.NuGetRoutes`, `NPMRoutes`, `GoRoutes`, `MavenRoutes`,
`PyPIRoutes`, `HelmRoutes` and `DockerRoutes`; each format package checks that
every pattern has both a status and principal row. The shared P8 reindex
envelope test remains in root because it covers several formats.
Its `statusFixture`, `matrixFixture`, and `newItemWorld` construct different
repository matrices and remain in the root while other route assertions move
to family packages under #724–#727. Their common auth, request, and package
primitives now come from `test/server/support`. Keep a root wrapper where a
shared helper has many existing callers; the wrapper must delegate rather than
maintain another copy of fixture behavior.
Until the remaining suites have their own packages, an impact planner must run
the root package whenever it could affect one of them. NuGet-only, npm-only,
Go-only, Maven-only, PyPI-only, classic Helm-only, Docker-only or Helm OCI-only
changes can select their respective `test/server/<family>` package as their server-route
package once #673 activates the impact map; shared server,
authentication, routing, store, and registration changes require all server
packages. A full qualification runs `go test ./...` and retains the original
coverage floors.

When moving another suite, preserve its assertions and server assembly path,
move or extract its fixtures with it, and demonstrate that `go test` can run the
new package independently. Then update the CI impact map and its dependency
tests before allowing CI to omit any root package work.
