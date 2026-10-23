# Server route test packages

Server-assembly tests exercise `internal/server.New` with the protocol routes,
authentication, repositories, and storage wired together. Protocol suites should
live in separate Go packages under `test/server/<family>`. This makes the test
package, rather than a test-name regular expression, the unit that CI can select.
A package may import `internal/server` and its public configuration surface;
fixtures used by that package live with it.

`test/server/support` owns reproducible authentication seeds, request helpers,
NuGet package bytes and Artifactory error decoding used by more than one suite.
It imports no server package, so both `internal/server` tests and independent
server-assembly packages can use it without an import cycle. Production code
must not import it. Server configuration and storage setup stay with each suite
because their repository and permission models differ.

`test/server/shared` contains the full-router authentication assertion.
`test/server/nuget` contains the NuGet v2/v3 status table, route × principal
matrix, API-key and force-authentication cases, symbols lifecycle, remote
status cases, and generated reindex method check. Run each independently with:

```sh
go test ./test/server/shared
go test ./test/server/nuget
```

The root `internal/server` package still contains cross-format tests and other
protocol-specific suites. NuGet-only route assertions no longer run from it.
The root registration and stub guards retain NuGet's implemented route patterns
through `test/server/support.NuGetRoutes`; the NuGet package checks that every
one of those patterns has both a status and principal row. The shared P8
reindex-envelope test remains in root because it covers several formats.
Its `statusFixture`, `matrixFixture`, and `newItemWorld` construct different
repository matrices and remain in the root while their route assertions move
to family packages under #718–#726. Their common auth, request, and package
primitives now come from `test/server/support`. Keep a root wrapper where a
shared helper has many existing callers; the wrapper must delegate rather than
maintain another copy of fixture behavior.
Until the remaining suites have their own packages, an impact planner must run the root
package whenever it could affect one of them. NuGet-only changes can select
`test/server/nuget` as their server-route package once #673 activates the
impact map; shared server,
authentication, routing, store, and registration changes require all server
packages. A full qualification runs `go test ./...` and retains the original
coverage floors.

When moving another suite, preserve its assertions and server assembly path,
move or extract its fixtures with it, and demonstrate that `go test` can run the
new package independently. Then update the CI impact map and its dependency
tests before allowing CI to omit any root package work.
