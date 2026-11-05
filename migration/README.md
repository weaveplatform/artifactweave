# migration/

This folder is the staged future `internal/`. During P9d every package moves from its
legacy path to the modular-monolith layout of
[decision 0044](../docs/research/decisions/0044-modular-monolith-migration.md), without
changing behaviour. The [plan's P9d section](../docs/implementation/plan.md#p9d-modular-monolith-migration)
orders the work, and the index issue
[#935](https://github.com/weaveplatform/artifactweave/issues/935) holds the product
baseline every ticket follows. This README, the ledger and its schema are deleted when the
folder is promoted.

## Staging rules

- **`migration/<x>` is the future `internal/<x>`.** A target package is built at its final
  relative path, with the import path `github.com/weaveplatform/artifactweave/migration/<x>`.
  A staged path has no `internal` element, so legacy code may import staged code and
  packages move bottom-up.
- **Legacy roots.** `internal/**` and `pkg/{aql,blob,cdn,layout,repo,store,types}/**` are
  legacy; they empty out as their packages move. `cmd/`, `api/`, `pkg/client`,
  `pkg/containertest`, `test/`, `tools/`, `deploy/` and `docs/` change in place.
- **Top-level directories.** Only `kernel`, `platform`, `gateway`, `app`, `layout`,
  `devtools` and the thirteen modules (`artifacts`, `access`, `configuration`,
  `notifications`, `retention`, `federation`, `releases`, `workers`, `evidence`,
  `governance`, `security`, `operations`, `estate`) may sit directly under `migration/`.
- **New code** goes under `migration/`, at its target path. Bug fixes to code that has
  not moved yet land at its current path.
- **Promotion.** At the end every `migration/<top>` is renamed to `internal/<top>`, every
  import path is rewritten, and `docs/implementation/p9d-path-map.json` records the final
  path of every legacy package and file.

### Walk rule

Every new repository walk uses `platform/testkit/repopath` (`SkipDir`, or `GoDirs` for
the directories that hold Go files). The walk never skips its root, and below the root it
skips:

- directories whose name starts with `.`: the untracked `.worktrees/` holds about 100
  repository copies, each with its own `go.mod`;
- `testdata`, `vendor` and `node_modules`;
- any directory that holds a `go.mod`, since it is another module.

Tests find the repository root with `repopath.Root` and read fixtures by root-relative,
slash-separated path with `repopath.Path` and `repopath.Read`, so a moved test keeps
finding its fixtures.

## Per-ticket checklist

Every ticket that creates, moves or splits a package:

0. Paths and line numbers in a ticket are as of `02affc88`. Once the move tool has
   merged, `go run ./tools/pkgmove status <legacy path>` prints where a package or file
   lives now.
1. Moves with `tools/pkgmove`: a `git mv` driven by the ledger, the package clause, the
   importer rewrite, the configuration path rewrite and the stale-reference check. A
   split package is split in place first and moved in a later commit, never both in one
   commit; `go run ./tools/pkgmove fix` requalifies importers.
2. Writes each package's `doc.go` to the
   [documentation standard](../docs/research/decisions/0044-modular-monolith-migration.md#documentation-standard),
   comments every exported identifier and removes comments that restate the code.
3. Lists the path couplings it touched. The move tool rewrites the fixed configuration
   list (`test_ownership.json`, `.testcoverage.yml`, `.gitattributes`, the format
   inventory's `route_file`, the client-recipes evidence, `x-go-type-import` in
   `api/src/**`, the Makefile, the workflows, the `-X` ldflags and the work queue's meter
   name), and the pull request shows `go run ./tools/pkgmove check` clean.
4. Changes no behaviour: the same routes, statuses, response envelopes, YAML
   configuration shape, CLI behaviour and database behaviour, with error, log and metric
   text byte-identical. A user-visible effect is announced with a `BREAKING CHANGE:`
   footer, as decision 0044 describes.
5. Verifies with `GOWORK=off go build ./...`, `make vet`, `make lint`, `make fmt-check`,
   `make verify-generate`, `make test` (or the moved and dependent packages) and the
   layout and doc tests. A full CI run (`ci:full`) must cover the change, with merged
   coverage at or above 95% total and 90% per package. The headroom is about three
   statements, so new code aims for 97% or more.
6. Opens one pull request against main, with a Conventional title
   (`refactor(<scope>): ...`, never `!`), squash-merged by the maintainer.

## The ledger

`ledger.json` maps every legacy package directory to its target and owning ticket. It
records intent only: the status of each entry is derived from the tree, so no change edits
the ledger to record progress. `migration/devtools/ledger` loads, validates, formats and
derives it, and `ledger.schema.json` describes it for editors; the Go code is
authoritative, and a test keeps the two in agreement.

### Fields

The header is fixed: `schema` is `1`, `module` is the module path, `staging_root` is
`migration` and `final_root` is `internal`. Then come three lists.

**`packages`**: one entry per legacy package directory.

| Field | Meaning |
|---|---|
| `from` | The legacy directory, relative to the repository root, such as `internal/server/npm` |
| `action` | `move`, `merge`, `split` or `delete` (see below) |
| `to` | The target directory; required for `move` and `merge`, absent otherwise |
| `scope` | `root` (the default): `to` is relative to the staging root, so it stays valid after promotion. `repo`: `to` is relative to the repository root and must start with `test/` |
| `name` | The package name, only where it is not the last element of `to`, such as `npmhttp` for `artifacts/ecosystem/npm/http` |
| `rename` | File renames on the way, old base name to new, such as `{"formats.go": "registry.go"}` |
| `ticket` | The owning ticket, such as `ECO-06` |
| `note` | Free text |

**`files`**: one entry per file that does not travel with its package. The ticket that
moves files out of a split package adds their entries; a file inside a `move` package
gets an entry only when it goes somewhere else, such as a test that moves to `test/`.

| Field | Meaning |
|---|---|
| `from` | The legacy file, or an asset directory with a trailing `/` |
| `action` | `move`, `split` or `delete` |
| `to` | The target directory; required for `move` |
| `scope` | As for packages |
| `file` | The new file name, where it changes |
| `targets` | For `split`: the target directories, at least two |
| `ticket`, `note` | As for packages |

**`placeholders`**: one entry per placeholder package (see below), with `to`, `scope`,
`phases` (the implementing phases, such as `P10`, at least one) and `note`.

### Actions

- **`move`**: the whole directory goes to `to`, which no other entry shares.
- **`merge`**: several directories go to one `to`; every entry sharing that target is a
  `merge`.
- **`split`**: the files go to several targets. The package entry has no `to`, `name` or
  `rename`; file entries record where its files go. Once one target remains, the owner
  may turn the entry into a `move`.
- **`delete`**: the package is removed, as `internal/layout` is, which `migration/layout`
  replaces.

### Validation

`Validate` reports every problem, not just the first: the patterns for paths, tickets,
phases, names and file names; unique `from` values across package and file entries, and
unique placeholders; the `move` and `merge` target rules above; a root-scope target that
starts with one of the top-level directories above; a repo-scope target that starts with
`test/`; a file entry inside a legacy directory that has a package entry; and no
placeholder that is the target of an entry.

### Derived status

| Entry | Status |
|---|---|
| Package `move` or `merge` | `done`: the legacy directory holds no `.go` files and the target holds some; `bridged`: the legacy directory holds only `bridge.go` or `bridge_*.go` files and the target holds code; `broken`: neither holds `.go` files; `pending` otherwise |
| Package `split` | `done`: no `.go` files left directly in the legacy directory; `bridged`: only bridge files left; `pending` otherwise |
| Package `delete` | `done`: no `.go` files left; `pending` otherwise |
| File `move` | `done`: the legacy path is absent and the target exists (a trailing `/` marks an asset directory); `broken`: both are absent; `pending` otherwise |
| File `split` or `delete` | `done`: the legacy path is absent; `pending` otherwise |
| Placeholder | `ok`: `doc.go` is the directory's only Go file; `implemented`: it holds Go files other than `doc.go`; `missing`: it holds no Go file, or does not exist |

Only the `.go` files directly in a directory count; subdirectories are judged by their own
entries.

`TestUnitLedgerComplete` in `migration/layout` fails on any validation error, a ledger
that is not in canonical form, a legacy package directory without an entry, any `broken`
entry, and a placeholder that is `missing` or `implemented`. With `-v` it logs the count
per status:

```sh
go test ./migration/layout -run '^TestUnitLedgerComplete$' -count=1 -v
```

### Canonical form

The header keys come first, in struct order. Then each list has one entry per line as
compact JSON in struct field order, with no HTML escaping; packages and files are sorted
by `from` and placeholders by `to`; the indent is four spaces and the file ends with a
newline. One entry per line keeps the conflicts of pull requests that add entries in
parallel on their own lines. `.gitattributes` checks JSON out with LF line endings, so the
byte comparison holds on every operating system.

### Adding or changing an entry

1. Edit `ledger.json`; order and spacing do not matter yet.
2. Rewrite it in canonical form and review the diff:

   ```sh
   ARTIFACTWEAVE_UPDATE_LEDGER=1 go test ./migration/layout -run '^TestUnitLedgerComplete$' -count=1
   git diff migration/ledger.json
   ```

3. Run `go test ./migration/... -run '^TestUnit' -count=1` and commit the ledger with the
   change that needs it.

Entries change when the plan changes, never to record progress:

- a legacy package that appears on main gets an entry, owned by the ticket that owns its
  neighbours;
- a ticket that moves files out of a split package adds their file entries;
- a placeholder gets its entry when it is created, and loses it in the change that gives
  it code.

## Bridges

A declaration that leaves a legacy split package, such as `pkg/types`, `pkg/store`,
`internal/auth` or `internal/authz`, leaves a type alias at its old path while its users
are re-pointed (Go's gradual code repair):

- one `bridge_<target>.go` per target, headed `// Bridge (P9d): declared in <path>; PRO-02 deletes it.`;
- never marked `Deprecated:`, because staticcheck SA1019 would then fail lint at every
  remaining call site;
- sentinel errors bridge as `var ErrNotFound = kernel.ErrNotFound`, the same pointer, so
  `errors.Is` keeps matching; generic functions bridge as wrappers;
- a `package X` test that would form an import cycle becomes `package X_test` with an
  `export_test.go`.

A legacy directory that holds only bridge files is `bridged`.
[PRO-01](https://github.com/weaveplatform/artifactweave/issues/1100) re-points every user
and [PRO-02](https://github.com/weaveplatform/artifactweave/issues/1101) deletes the
bridges.

## Placeholders

Every package the target layout marks for a later phase exists from the start as a
directory with `doc.go` only: no code and no API until its phase. Its Status reads
`Placeholder: implemented by Pnn.`, naming that phase. The `access`, `artifacts` and
`configuration` module roots read `Placeholder: implemented by P9d.` and have no ledger
entry, because P9d adds their constructors.

A change that adds any file other than `doc.go` to a placeholder directory removes the
placeholder's ledger entry, if it has one, and rewrites its Status in the same pull
request; `TestUnitLedgerComplete` and `TestUnitPackageDocs` fail otherwise. A placeholder
is judged by its own directory; its subdirectories do not count.

## History and living documents

History documents keep the paths they were written with, and the move tool never rewrites
them:

- the five architecture-option research documents in
  [`docs/research/architecture-options/`](../docs/research/architecture-options/), with
  [`5-modular-monolith.md`](../docs/research/architecture-options/5-modular-monolith.md)
  as the target layout;
- `CHANGELOG.md`, the top-level research documents (`docs/research/*.md`),
  `docs/research/evidence/**`, decision 0044, `docs/testing/evidence/**` and
  `docs/testing/phase*.md`;
- `docs/implementation/repo-layout.md`, until the close-out rewrites it, and
  `docs/implementation/p9d-path-map.json`;
- this README and the ledger files.

Every other tracked Markdown file is living, including `docs/implementation/plan.md` and
the other decision records. The move tool rewrites legacy paths in living documents as
packages move, and its stale check fails on any legacy path that remains once its entry is
done. A living document therefore names a legacy package by its role, such as "the old
server package", where the reference must outlast the move.

## Pre-migration base

The pre-migration base is `ec4592a513985e4986f60b060984e40c3347be73`, the parent of the
squash commit that brought `migration/` into CI
([#1105](https://github.com/weaveplatform/artifactweave/pull/1105), `0e5def29`):

```sh
git rev-parse 0e5def29^
```

The acceptance features at this commit are the reference that
[PRO-05](https://github.com/weaveplatform/artifactweave/issues/1104) compares the
promoted tree against.
