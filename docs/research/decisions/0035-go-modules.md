# 0035: Go modules

Status: Proposed

## Context

The go command fetches modules through the GOPROXY protocol, and `jf go-publish` deploys
them to Artifactory's `/api/go/<repo>` with one PUT per file. JFrog documents no REST
operation for either, Artifactory's Go handler is closed source, and its documentation
covers only client set-up. Decision [0032](0032-package-format-handlers.md) fixes how a
format plugs in, decision [0003](0003-repository-model.md) what a remote or virtual
repository answers before P9 and P10, and decision [0018](0018-package-index.md) the
package index; this decision fixes what a local Go repository answers and stores.
[Research 26](../26-go-modules.md) holds the facts, and its section numbers are cited below
as R26.

## Decision

### Packages and routes

The HTTP layer is `internal/server/golang`, mounted through the protocol router kit of
decision 0032 at `/artifactory/api/go/{repoKey}/`. Path decoding, validation, version
selection and the package-index hooks are `internal/format/golang`, built on
`golang.org/x/mod/module` and `golang.org/x/mod/semver`.

After the repository key, a path is split at its last `/@v/` or `/@latest`. A module path
cannot contain `@` (R26 1.2), so the split is unambiguous:

| Method | Path | Answer |
|---|---|---|
| `GET` | `<module>/@v/list` | The listed versions (below) |
| `GET`, `HEAD` | `<module>/@v/<version>.info` | The version's `.info`, stored or synthesised |
| `GET`, `HEAD` | `<module>/@v/<version>.mod`, `.zip` | The stored file |
| `GET` | `<module>/@latest`, `<module>/@v/latest.info` | The latest version's `.info` |
| `PUT` | `<module>/@v/<version>.zip`, `.mod`, `.info` | A deploy (below) |
| any | `sumdb/...` | 404 (below) |

Any other path answers 404, and any other method on a listed path 405.

### Module paths, versions and storage

A module path or version in a URL is decoded with `module.UnescapePath` or
`module.UnescapeVersion` when it contains `!`, and taken as written otherwise. The go
command sends the case-encoded form and `jf` the plain one (R26 1.3, 3.1); both name the
same version. A path that fails to decode answers 404 on a read and 400 on a PUT.

A version is stored at `<module>/@v/<version>.<ext>` with the module path and version
**not** case-encoded, as `jf go-publish` sends them.

### Reads

**A version is visible** to Go reads once both its `.mod` and its `.zip` are stored. Until
then every Go read of it answers 404, and its files stay reachable through the item API.

- **`@v/list`** answers `text/plain; charset=utf-8`, one version per line, each ending in
  `\n`, in ascending semantic-version order. It holds every visible version of the module
  that is not a pseudo-version and whose `.mod` the caller can read. A module whose
  readable versions are all pseudo-versions answers 200 with an empty body. A module with
  no readable visible version answers 404.
- **`.info`** serves the stored `.info` as it is, through the item download path. When none
  is stored, the answer is synthesised as `{"Version":"<version>","Time":"<time>"}` with
  `Content-Type: application/json`, where the time is when the version was created in the
  package index, in RFC 3339 and UTC. A `.info` request for anything but a visible
  canonical version, such as a branch name, answers 404.
- **`.mod` and `.zip`** are served through the item download path, so decision
  [0029](0029-cdn-delivery-and-download-redirect.md)'s redirect, `Range` and the download
  counts apply. A `.mod` is never synthesised.
- **`@latest` and `@v/latest.info`** answer the `.info` of one visible version the caller
  can read, chosen as the go command prefers (R26 1.3): the highest release; when there is
  none, the highest pre-release that is not a pseudo-version; when there is none, the
  pseudo-version with the latest time it encodes (`module.PseudoVersionTime`). With no such
  version the answer is 404. `@v/latest.info` exists because `jf go` asks for it (R26 1.3).

### Deploys

`PUT .../<module>/@v/<version>.<ext>[;key=value...]` deploys one file.

- **Checks**, each answering 400 with the reason. The first two are the **path checks**:
  - `<ext>` is `zip`, `mod` or `info`;
  - `module.Check(module, version)` passes and the version equals
    `module.CanonicalVersion(version)`;
  - a `.info` is a JSON object of at most 64 KiB whose `Version` equals the path's version,
    and whose `Time`, when present, is RFC 3339.

  The contents of a `.zip` and a `.mod` are not inspected.
- **Properties:** the matrix parameters become properties, decoded as on the item routes;
  `go.version` is stored like any other. The server adds none of its own.
- **Checksums:** the `X-Checksum-Sha1`, `-Sha256`, `-Md5` and `X-Checksum` headers are
  checked as on the item routes.
- **Answer:** 201 with the item routes' deploy JSON and the `X-Checksum-Sha256`, `-Sha1`
  and `-Md5` headers of the stored content (decision 0032's deploy helper).

**Republishing.** A version's `.zip` and `.mod` cannot change once stored:
- Different content (by SHA-256) over an existing `.zip` or `.mod` answers **409**, naming
  the module, version and file and saying that the version must be deleted to replace it.
  This holds on every write path into a Go repository: the `/api/go/` PUT, the item
  routes' PUT, and the target of a copy or a move, through the format hooks of decision
  0032.
- On the `/api/go/` PUT, the same content answers 201 and changes nothing but the
  properties, which are merged into the stored file's as a deploy over an existing file
  merges them.
- A `.info` may be replaced.

**Writes through the item routes** are stored whatever their path, as on any repository.
A file at `<module>/@v/<version>.<ext>` whose path passes the path checks is indexed; any
other file is stored and not indexed.

### Package index

This sets the Go part of decision 0018:
- **Package:** the module path, as stored, keyed by its repository.
- **Version:** the version string, created when its first file is stored.
- **Files:** the version's `.zip`, `.mod` and `.info`.
- **Order:** `CompareSemver` (`types.ComparatorFor(repo.Go)`). It drops build metadata, so
  `+incompatible` does not affect order; a module path admits either `vN.x.y` with its
  `/vN` suffix or `vN.x.y+incompatible` without one, never both, so no two versions
  compare equal. `@latest` selects with its own rule above.
- **Derived work:** none. The version list and `@latest` are computed on read from the
  index (decision 0032).
- **Reindex:** rebuilds a repository's Go index from the files whose paths pass the path
  checks.

### The `go-default` layout

`go-default` stays `[orgPath]/[module]/@v/[baseRev].[ext]`, not Artifactory's
`[orgPath]/[module]/@v/v[refs].zip` (R26 4.2), so layout-based searches and layout
translation see Go versions. Go reads never use the layout; they read the package index.

### Errors

Every error under `/artifactory/api/go/` is plain text, `text/plain; charset=utf-8`, through
`apierror.Text`: one English sentence naming the module and version where there is one.

| Status | When |
|---|---|
| 400 | a PUT fails its checks |
| 401 | authentication is needed, with the challenge of decision [0005](0005-authentication-and-tokens.md) |
| 403 | the caller lacks the permission |
| 404 | an unknown repository, a repository of another package type, an unknown or invisible module or version, a path that is no Go operation, and `sumdb/` |
| 405 | another method on a Go path |
| 409 | a `.zip` or `.mod` republished with different content |
| 501 | a remote or virtual Go repository, naming its class (decision 0003) |

### Permissions

- Reads of `.zip`, `.mod` and a stored `.info` need Read on that file, as on the item
  routes. A synthesised `.info` needs Read on the version's `.mod`.
- `@v/list` and `@latest` consider only versions whose `.mod` the caller can read.
- A PUT on `/api/go/` needs Deploy. Replacing a `.info` needs Delete as well, as an
  overwrite does on the item routes; repeating a `.zip` or `.mod` with the same content
  needs only Deploy. The item routes keep their own rule: an overwrite needs Delete.

### The checksum database

Every request under `<repo>/sumdb/` answers 404 before authentication and before the
repository is looked up, so the go command consults `sum.golang.org` directly, or nothing
for modules matched by `GONOSUMDB` or `GOPRIVATE` (R26 2). The first element of a module path
must contain a dot, so `sumdb` never names a module.

## Rationale

**Storage form.** `jf go-publish` deploys and records in its build-info the plain module
path and version (R26 3.1), so storing that form keeps the tree, the build's artifact paths
and AQL searches by path in agreement. Accepting both spellings on input costs nothing: `!`
is never part of a valid module path and upper case is never part of an encoded one.

**Visibility.** `jf` deploys `.zip`, then `.mod`, then `.info`. A version listed after the
first PUT would answer 404 for its `.mod` until the second; the go command takes 404 as
"not here", and with `|direct` would fetch the module from its source instead. Waiting for
both files closes that gap. `.info` comes last and is skipped against an Artifactory older
than 6.10 (R26 3.1), so it is synthesised when missing.

**No synthesised `.mod`.** The protocol's synthesised `.mod` is for code without a `go.mod`
fetched from its source, which only a remote meets (P9). `jf go-publish` refuses a project
without a `go.mod`, so a missing `.mod` means an incomplete deploy.

**`@latest`.** `@v/list` leaves out pseudo-versions, as the protocol asks, and the go
command's fallback without `@latest` finds the latest version only from pseudo-versions or
timestamps in the list (R26 1.3). Without `@latest` a module published only at
pseudo-versions could not be resolved.

**Republishing.** `go.sum` and the checksum database pin a version's `.zip` and `.mod`
forever, so new content under an old version fails every client that has fetched it.
Refusing different content protects them. Accepting identical content lets a pipeline run
`jf go-publish` again after a later step failed: the `.zip` and `.mod` are the same and only
the `.info`'s time changes (R26 3.1). Refusing every republish, as the CLI's help says
Artifactory does, would fail such a re-run at its first PUT. This departs from Artifactory
and is DV-039 in the [deviations register](../../implementation/deviations.md).

**Plain-text errors.** The go command shows an error's body only when it is plain text
(R26 1.1).

**The checksum database.** The sumdb proxy is cut from v1 (research 10). The go command
takes any answer other than 200, 404 or 410 to its probe as the database being unavailable
and fails (R26 2), so a 401 to an anonymous probe would break every lookup.

**Layout.** Artifactory's `go-default` contains a token its own parser drops, so it matches
no module file and gives no version (R26 4.2). Keeping a layout that matches is DV-040 in
the [deviations register](../../implementation/deviations.md).

**Alternatives considered:**
- *Storing case-encoded paths*: `jf`'s PUT paths would need rewriting, and the build-info
  `jf` publishes would name paths that do not exist.
- *Listing a version once its `.zip` is stored*: opens the gap between PUTs described above.
- *Synthesising `.mod` as `module <path>`*: would serve a `go.mod` the publisher never wrote,
  whose hash then disagrees with any later real one.
- *Overwriting with Delete, as the item routes do*: a version's content could change under
  clients that have pinned its hash.
- *Validating `.zip` contents with `zip.CheckZip` on upload*: needs the stored zip read back
  in full. `jf` builds its zips with `zip.Create`, which enforces the same rules, and the go
  command checks every zip it extracts (R26 1.3).
- *The Artifactory JSON error envelope*: the go command would print only the status.
- *Copying Artifactory's `go-default` pattern*: it matches nothing.

## Constraints

- A `.zip` of any size is accepted; the go command refuses one over 500 MiB when it
  downloads it.
- `go-default` splits a pre-release whose later dot-separated part starts with a letter,
  such as `v1.0.0-alpha.beta`, wrongly, and does not match a module path with a single
  element, such as `example.com` (R26 4.2). Layout-based searches miss those versions; Go
  reads are unaffected.
- A synthesised `.info` gives the time of the deploy, not of the commit.
- `jf go-publish` writes `.info` times in local time followed by `Z` (R26 3.1), and they are
  served as written.
- A version whose deploy stopped between PUTs stays invisible until its missing file is
  deployed or the version is deleted.
- Remote and virtual Go repositories answer 501 until P9 and P10; the sumdb proxy and
  VCS-backed remotes are v1 cuts (research 10).
- What Artifactory stores and answers in R26 section 6 is inferred and on the live-capture
  list (#216) for P19.

## Verification

- `internal/format/golang` unit tests: path decoding with encoded, plain and invalid
  spellings; each PUT check; visibility; `@v/list` and `@latest` over releases,
  pre-releases, pseudo-versions and `+incompatible`; the index after deploy, overwrite,
  delete, copy, move, restore and reindex, through the shared contract suite of decision
  0032 (#268).
- `internal/server/golang` tests in the per-format status table and route × principal
  matrix of decision 0032: every status in the table above, `text/plain` error bodies, the
  201's `X-Checksum-*` headers, the identical republish and the 409, replacing a `.info`
  with and without Delete, `sumdb/` answering 404 to an anonymous caller, 501 for a remote
  and a virtual, and 404 for a repository of another type.
- Acceptance (decision [0015](0015-testing-and-acceptance.md)): the runner's Go toolchain
  with `GOPROXY` set to the repository and no `direct`, `GONOSUMDB` covering the test
  modules, `GOTOOLCHAIN=local` and `GOFLAGS=-modcacherw`. `jf go-config`, then
  `jf go-publish` with a build name and number and `jf rt build-publish`, then
  `go mod download` and `go get` of a module with an upper-case path element, a `/v2`
  module and a pseudo-version; a repeated identical `jf go-publish` succeeds, a changed one
  fails with 409; `jf go` resolves `@v/latest.info`.

## References

- [Research 26](../26-go-modules.md)
- Go Modules Reference, GOPROXY protocol: <https://go.dev/ref/mod#goproxy-protocol>
- Checksum database design: <https://go.googlesource.com/proposal/+/master/design/25530-sumdb.md>
- go1.27.1 `src/cmd/go/internal/modfetch/proxy.go`, `src/cmd/go/internal/web/http.go`
- `golang.org/x/mod` v0.42.0, `module/module.go`, `zip/zip.go`
- jfrog-client-go `0646d7b50f9a`, `artifactory/services/go/publish.go`
- jfrog-cli-artifactory `4a0f3827cfa0`, `artifactory/commands/golang/`
- <https://docs.jfrog.com/artifactory/docs/go-modules.md>
