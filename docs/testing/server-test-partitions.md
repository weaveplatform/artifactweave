# Root server test partitions

The root `internal/server` Go package has protocol-specific tests mixed with
shared API tests. `tools/servertests` supplies an exact `go test -run` pattern
for a protocol family. The checked-in
[`ownership.json`](../../tools/servertests/ownership.json) assigns each root
server test file to one family, `common`, or `shared`.

For a format-scoped run, select that family's files plus the `common`
registration and format-contract smoke tests. Shared auth, routing, store,
API/configuration and other cross-format changes need the full root package.
A full release or manual qualification still runs every test. The selector
parses every Go test file and fails if the inventory omits a new file, contains
an invalid owner, or selects no tests. The impact planner must treat selector
failure as a full-gate fallback; it must not silently continue with an empty
pattern. Review ownership when adding or renaming a root server test file.

To inspect the selection from the repository root:

```sh
go run ./tools/servertests -family=nuget >/tmp/nuget-server-pattern
```

The command prints the selected/total count to stderr and a single anchored
regular expression to stdout. The CI planner in #673 will combine this
partition with Go reverse dependencies, dedicated `internal/server/<family>`
tests, contract tests and native-client scenarios. A filename is not the sole
impact boundary.
