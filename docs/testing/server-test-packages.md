# Server route test packages

Server-assembly tests exercise `internal/server.New` with the protocol routes,
authentication, repositories, and storage wired together. Protocol suites should
live in separate Go packages under `test/server/<family>`. This makes the test
package, rather than a test-name regular expression, the unit that CI can select.
A package may import `internal/server` and its public configuration surface;
fixtures used by that package live with it.

`test/server/nuget` contains the NuGet client write and force-authentication
route tests moved from the root `internal/server` test package. Run them with:

```sh
go test ./test/server/nuget
```

The root `internal/server` package still contains cross-format tests and other
protocol-specific suites, including NuGet symbol, status, and matrix tests.
Until those suites have their own packages, an impact planner must run the root
package whenever it could affect one of them. It must not drop root coverage
based on the package above or infer ownership from test names. Shared server,
authentication, routing, store, and registration changes require all server
packages. A full qualification runs `go test ./...` and retains the original
coverage floors.

When moving another suite, preserve its assertions and server assembly path,
move or extract its fixtures with it, and demonstrate that `go test` can run the
new package independently. Then update the CI impact map and its dependency
tests before allowing CI to omit any root package work.
