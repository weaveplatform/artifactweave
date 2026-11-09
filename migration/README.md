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

0. Paths and line numbers in a ticket are as of `02affc88`;
   `go run ./tools/pkgmove status <legacy path>` prints where a package or file lives now.
1. Moves with `tools/pkgmove` (see [Moving packages with pkgmove](#moving-packages-with-pkgmove)):
   a `git mv` driven by the ledger, the package clause, the importer rewrite, the
   configuration path rewrite and the stale-reference check. A split package is split in
   place first and moved in a later commit, never both in one commit;
   `go run ./tools/pkgmove fix` requalifies importers (see
   [Splitting a package](#splitting-a-package)).
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
   text byte-identical, except where decision 0044 says otherwise.
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

A declaration that leaves a legacy split package, such as `pkg/types`, `pkg/store` or
`internal/auth`, leaves a type alias at its old path while its users are re-pointed
(Go's gradual code repair):

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
- this README and the ledger files;
- `devtools/ledger/ledger.go`, whose `LegacyRoots` keeps naming each `pkg/` root after
  that root's own package moves, until promotion.

Every other tracked Markdown file is living, including `docs/implementation/plan.md` and
the other decision records. The move tool rewrites legacy paths in living documents as
packages move, and its stale check fails on any legacy path that remains once its entry is
done. A living document therefore names a legacy package by its role, such as "the old
server package", where the reference must outlast the move.

## Moving packages with pkgmove

`tools/pkgmove` carries out the ledger. It reads the ledger and the tracked files only, so
the untracked copies in `.worktrees/` never count.

```sh
go run ./tools/pkgmove status  [-ticket ID] [-json] [legacy-path]
go run ./tools/pkgmove move    -ticket ID [-dry-run] [-no-regenerate] [-skip GLOB]...
go run ./tools/pkgmove move    -from DIR -to DIR [-name NAME] [-dry-run] [-no-regenerate] [-skip GLOB]...
go run ./tools/pkgmove check   [-ticket ID | -from DIR]
go run ./tools/pkgmove fmt     [-check]
go run ./tools/pkgmove promote [-dry-run] [-no-regenerate]
go run ./tools/pkgmove fix     -ticket ID [-report-only] [-no-regenerate] [-external FILE...]
go run ./tools/pkgmove fix     -bridges [-report-only] [-no-regenerate]
```

The exit status is 0 on success, 1 when the command found problems (printed to stdout as
`file:line: message`) or refused (the reason is on stderr), and 2 for a command line it
cannot run.

| Command | What it does |
|---|---|
| `status` | One line per entry with its derived status and a count per status. Given a legacy path (a package directory, a file in one, or a file entry) it prints where that path lives now, renames applied, such as `internal/server/npm/npm.go (pending, ECO-06; moves to migration/artifacts/ecosystem/npm/http/npm.go)`. `-ticket` keeps one ticket's entries; `-json` prints objects `{ticket, from, to, action, status}` |
| `move -ticket` | Moves every pending `move` and `merge` entry of the ticket in one change, then its pending file `move` entries. The ticket's `split` and `delete` entries, package or file, are named in a notice and left alone |
| `move -from -to` | Re-homes a directory below `migration/` or `test/` that has no ledger entry, such as a staged package a later ticket puts elsewhere; `-name` sets the package name when it is not the last element of `-to` |
| `check` | The stale-path step of CI: reports every remaining mention of an old path. `-ticket` checks one ticket's done entries; `-from` checks a re-homed directory |
| `fmt` | Rewrites the ledger in [canonical form](#canonical-form); `-check` only reports a difference |
| `promote` | The close-out ([PRO-03](https://github.com/weaveplatform/artifactweave/issues/1102)): renames each `migration/<top>` to `internal/<top>`, rewrites the staged paths, removes this README and the ledger files and writes `docs/implementation/p9d-path-map.json` |
| `fix -ticket` | Repairs what the ticket's file moves broke: each identifier they left unresolved, in a moved file, in the package it left or in an importer, is pointed at the package that now declares it, and what needs a hand edit is reported. `-external FILE...` converts test files to the external test package. See [Splitting a package](#splitting-a-package) |
| `fix -bridges` | Points every use of a [bridge](#bridges) declaration at the declaration it aliases and reports the uses it cannot rewrite. [PRO-01](https://github.com/weaveplatform/artifactweave/issues/1100) runs it across the tree; where no legacy package holds a bridge it changes nothing |

`-dry-run` prints the plan, every file move, write and regeneration step, and changes
nothing; `-report-only` does the same for `fix`. Run `move -dry-run` first and read the
plan.

### What a move does

`move` and `promote` run from the module root and refuse, changing nothing, unless the
ledger validates and the working tree has no uncommitted change to a tracked file. `move`
also refuses an entry that is not `pending`, untracked files in a source directory (`git
mv` would leave them behind), a target directory of a package entry that already holds Go
files (a target with placeholder subpackages below it is fine), two files that would land
on one path, a file entry that would overwrite a tracked file, a file entry whose `from`
the tree does not hold in that shape (an asset directory ends in `/`, a file does not), a
moved test that declares a fuzz target `FuzzX` while no file entry of the ticket moves its
seed corpus `testdata/fuzz/FuzzX/` to the new directory's `testdata/fuzz`, a package
rename that a wrapcheck exception in `.golangci.yml` matches by name, and importers of a
renamed package that do not type-check for linux, darwin and windows. Then it:

1. moves with `git mv` the files directly in each directory, with the entry's `rename`
   applied, and its asset subdirectories, such as `testdata/` and `migrations/`, that hold
   no package at any depth; a subdirectory holding a package stays for its own entry, and
   a file with a file entry stays for that entry. Then it moves each file of the ticket's
   file `move` entries to the target directory, under the entry's `file` name when it has
   one, and each tracked file of an asset directory entry;
2. rewrites the package clause to the ledger `name`, or the last element of `to`. A file
   that a file entry moves takes the package name of its target directory: that of the
   Go files there, else that of the package entry that creates it, else its last element.
   A test keeps its style, except that an in-package test that joins a directory whose
   tests are all external tests, as under `test/`, becomes one (`<name>_test`), with a
   notice;
3. rewrites each importer's import path. Where the package name changes, it renames the
   qualifiers from type information, or keeps the old name as an import name where the
   new one would clash in that file; an import that a merge repeats is removed;
4. rewrites the old paths in Go string literals and comments, in the living Markdown and
   in the fixed configuration list: `.golangci.yml`, `.testcoverage.yml`,
   `.gitattributes`, the Makefiles, `Dockerfile`, `.goreleaser.yaml`, the workflows, the
   OpenAPI sources and generator configuration under `api/`, `tools/depsync`, the
   acceptance scripts and features, the format inventory, the client recipe sources and
   the SQL migrations. Generated `*.gen.go` files and the history set are never edited;
   `-skip GLOB` (slash-separated, `**` for any depth, repeatable) exempts more files from
   the text rewrite, but not from the import rewrite or the stale check;
5. rewrites `tools/citestselection/test_ownership.json` as JSON in its own encoding: a
   moved test directory keeps its owner under the new key, and a key whose new path is no
   test directory is dropped with a note. A test file that a file entry moves follows
   [Moving a root test](../docs/testing/server-test-ownership.md#moving-a-root-test): its
   name leaves the old directory's `shared_files`, its new directory joins the inventory,
   and its format dependencies follow it or go, with a note. When a moved test declares a
   fuzz target, the Makefile's `FUZZ_PKGS` lists its new directory, in place of the old
   one once no fuzz target remains there;
6. runs the regeneration steps the change needs, in order, unless `-no-regenerate` is
   given: `make generate` when an API source, a generator or a generated file changed or
   generated code names a moved path, `go run ./tools/formatinventory` when the format
   inventory changed, `make client-recipes` when a recipe source changed or the evidence
   lock names a moved path, `make api-ledger` when the API reliability ledger names one,
   and `make fmt` always;
7. runs `check -ticket` for the moved entries.

Moved files are staged; edited files are not. Review the change with `git status` and
`git diff`, run the verification of the [per-ticket checklist](#per-ticket-checklist) and
commit.

### What counts as a reference

One matcher decides every reference for `move`, `check`, `promote` and `status`, so a
move rewrites exactly the paths `check` would report. For an old path `P`:

- `<module>/P`, `$(MODULE)/P` and `${MODULE}/P` match anywhere. A bare `P` matches only
  where a path can start (the start of the text, after `^`, after a `./` or `../`
  chain, or after a byte that cannot be part of a path, such as a space, a quote or `=`)
  and only when the path ends there or continues with `/`. So `internal/format` never
  matches `internal/formats`, `pkg/x/internal/format` or a tracked file such as
  `internal/format.go`;
- `P/<file>` is rewritten with the entry's renames applied; a subpackage with its own
  entry is judged by that entry, never through its parent;
- a directory prefix such as `"P/"` and a `P/...` pattern are rewritten only once every
  package below `P` lands at the same place below the target. Until then they have no
  single replacement and are neither rewritten nor reported, so the ticket that empties
  such a directory checks them by hand.

Tests name fixtures so that a move finds them: one slash-separated, root-relative
literal, as `repopath.Path(t, "internal/x/testdata/a.json")`, `repopath.Read`, or a
literal prefix joined to a name (`"internal/x/testdata/" + name`). godog steps report a
failure by returning an error and hold no `testing.TB`, so they join a root-relative
literal to `repoRoot`, which `TestMain` in `test/acceptance` resolves once with
`repopath.FindRoot`: `filepath.Join(repoRoot, "internal/x/testdata", name)`. A path
built from separate elements, or relative to the test's own directory, is not found.

`check` reads every tracked text file outside the history set, not only the files that
`move` rewrites, so a mention in another kind of file is reported for a hand edit.

### Splitting a package

A split package's files go to several targets, so `move` takes them file by file
(`pkgmove` below is `go run ./tools/pkgmove`):

1. Split the files in place first, in their own commit: a file whose declarations go to
   different targets is cut into one file per target, so that each file moves whole.
2. Add a file entry for each file that the ticket moves, with the ticket as owner, and an
   asset directory entry, ending in `/`, for each fixture directory that goes with them,
   such as the seed corpus of a moved fuzz target. Run `pkgmove fmt`.
3. Run `pkgmove move -ticket ID -dry-run`, read the plan, then `pkgmove move -ticket ID`.
   The tree does not build yet: the moved files and their old package lost sight of each
   other.
4. Run `pkgmove fix -ticket ID -report-only`, then `pkgmove fix -ticket ID`. An
   identifier that the move left unresolved is pointed at the package that now declares
   it, whether it is in a moved file, in a file of the package it left or in an importer's
   `old.X`: exported, it gets that package's qualifier and import (with an explicit name
   where the package's own name means something else in the file), or no qualifier in
   that package's own files; an import whose every use moved goes. A name that an
   in-package test file took along, as an `export_test.go` does, is in sight of the
   external tests of its new directory only, so it is pointed at there and reported
   anywhere else. A moved file's uses of its old package's bridges, bare or qualified,
   are pointed at the bridges' targets.
5. Resolve the report by hand. Each line is `file:line: message`, for an unexported or
   test-only declaration that now lies across a package boundary, a method whose receiver
   type went elsewhere, an import that closes a cycle, a test that reads its package's own
   source (as `filepath.Glob("*.go")` does), a `//go:embed` pattern that matches nothing,
   a generated file (change its generator and run `make generate`) and a file that no
   load compiles. An in-package test that would close an import cycle becomes an external
   test with `pkgmove fix -ticket ID -external FILE...`, which reports each unexported
   name it uses for an `export_test.go`. The files of one run form the external test
   package together, so what one of them declares stays in sight of the others, and only
   names from the package's other files are reported.
6. Run `pkgmove check`, then the verification of the
   [per-ticket checklist](#per-ticket-checklist), and commit.

`fix` loads every package the split touches and every importer of the old ones, for
linux, darwin and windows with the e2e tag, and works from their type errors and from the
moved files' uses of bridges. It refuses a ticket whose file `move` entries have not
moved, or that has none, unless `-external` gives it files to convert.

`fix -bridges` is the same repair for [bridges](#bridges): each use, outside the bridge
files, of a declaration in a `bridge.go` or `bridge_*.go` file of a legacy package is
pointed at the declaration it aliases: a type, a var or const or, for a wrapper whose
signature is the target's and whose body only calls it with its own parameters, the
function it calls. It reports a declaration that aliases nothing it can name, an embedded
field whose name would change, an assignment to a var bridge and a use in a file that no
load compiles. It leaves the bridge files alone; PRO-02 deletes them.

### Known limits

- `split` and `delete` entries, package or file, are carried out by hand.
- Relative Markdown links inside moved files and GitHub URLs to repository files are not
  rewritten.
- Merging two packages where one imports the other leaves a self-import to remove by hand.
- A test that uses legacy paths as data, such as the keys of a map literal, can end up
  with two equal keys after the rewrite; the build reports it.
- With `-no-regenerate`, generated files still name the old paths, and `check` reports
  them until `make generate` runs.
- `fix` does not move declarations: what it reports is left for a hand edit, and a name
  that a moved file declares again in its new package is the compiler's to report.
- `promote` rewrites `<module>/migration/` in all three forms and bare `migration/<top>`
  paths; patterns that name `migration` otherwise, such as `^(internal|migration)/` in
  `.golangci.yml` or `^migration/[a-z]+/` in `.testcoverage.yml`, and `./migration/...`,
  are edited by hand. Promotion removes the ledger, so the same change removes the CI
  stale-path step, `tools/pkgmove`, the package behind it and that package's `TestMain`
  owner in `tools/testnames`.

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
