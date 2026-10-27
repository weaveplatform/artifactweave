# Go modules

Research baseline: **2026-10-16**.

Checked against: **Artifactory 7.161.32** (self-managed; SaaS 7.174.0) on 2026-10-17, by [#341](https://github.com/weaveplatform/artifactweave/issues/341); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)); [the 7.x check](#8-artifactory-7x-check) records it.

Phase P7 serves Go modules from local `go` repositories. [Research 04](04-package-protocols.md)
sketched the GOPROXY protocol and said nothing in it was unverified. Planning on 2026-10-16
found otherwise: Artifactory's Go support is closed source, JFrog documents only how to
point clients at it, and the `go-default` layout artifactweave bundles differs from the one
Artifactory 6.20 ships. This report collects what the Go documentation, the go command, the
JFrog CLI and Artifactory's open-source edition show. Decision
[0035](decisions/0035-go-modules.md) records the choices.

**Roadmap ownership (2026-10-21).** Sumdb proxying and VCS-backed Go remotes remain
unimplemented and are owned by P28, depending on P9/P10 and shared P33 VCS work.
They are active protocols, not superseded features. Decision
[0035](decisions/0035-go-modules.md#owned-follow-up) records the required integrity
and native-client proof; current local acceptance disables the checksum database.

Each claim is labelled:

| Label | Means |
|---|---|
| **[D]** | documented at the cited URL, read on 2026-10-16 |
| **[S]** | seen in source code at the cited commit |
| **[J]** | recorded in JFrog's public Jira, in the cited issue, read on 2026-10-18 |
| **[I]** | inferred from the documented facts; not stated by any source |

Sources:
- **The Go Modules Reference**, <https://go.dev/ref/mod>: "Module proxies" (the GOPROXY
  protocol), "Version queries", "Pseudo-versions", "Compatibility with non-module
  repositories", "Module zip files", "Private modules", "Authenticating modules" and
  "Environment variables".
- **The checksum database design**,
  <https://go.googlesource.com/proposal/+/master/design/25530-sumdb.md>.
- **The go command** at go1.27.1: `src/cmd/go/internal/modfetch/proxy.go`,
  `src/cmd/go/internal/modfetch/fetch.go`, `src/cmd/go/internal/web/http.go` and
  `src/cmd/go/internal/web/api.go`.
- **`golang.org/x/mod`** v0.42.0, the version artifactweave's `go.mod` pins:
  `module/module.go`, `module/pseudo.go` and `zip/zip.go`.
- **JFrog's documentation:** <https://docs.jfrog.com/artifactory/docs/go-modules.md>.
- **The JFrog CLI** `jf` 2.126.0 and the modules it pins: jfrog-cli-artifactory
  `4a0f3827cfa0` (`artifactory/commands/golang/`: `gopublish.go`, `publish.go`, `go.go` and
  `archive.go`), jfrog-client-go `0646d7b50f9a` (`artifactory/services/go/`, and
  `AddChecksumHeaders` in `artifactory/services/utils/artifactoryutils.go`), and jfrog-cli
  `eb1d5eb7ca97` (`GetAIDescription` in `docs/buildtools/gopublish/help.go`).
- **Artifactory 6.20's open-source edition**, the `knottnhupfer/artifactory-oss` mirror at
  `b99e5649bcad` that research 22 used, under `base/config/src/main/`:
  `java/org/artifactory/version/converter/v213/GoDefaultLayoutConverter.java`,
  `resources/META-INF/default/artifactory.config.xml`,
  `java/org/artifactory/util/RepoLayoutUtils.java` and
  `java/org/artifactory/mime/version/converter/v12/GoMimeTypeConverter.java`; and
  `base/common/src/main/java/org/artifactory/addon/go/`.

Artifactory's Go repository handler is not in the open-source edition: `addon/go` holds only
the interfaces the UI reads (`GoAddon`, `GoInfo`, `GoMetadataInfo`, `GoDependency`,
`GoGitProvider`). Artifactory's own behaviour below is documented or seen in its clients;
where neither shows it, the claim is **[I]** and the point is on the live-capture list
([#216](https://github.com/weaveplatform/artifactweave/issues/216)).

## 1. The GOPROXY protocol

### 1.1 Endpoints

- **[D]** A proxy answers GET requests under its base URL. `$module` and `$version` are
  case-encoded (section 1.2):

  | Path | Answer |
  |---|---|
  | `$base/$module/@v/list` | Plain text, one version per line. "This list should not include pseudo-versions." |
  | `$base/$module/@v/$version.info` | JSON `{"Version", "Time"}`. "The Version field is required and must contain a valid, canonical version." "Time is optional", in RFC 3339 |
  | `$base/$module/@v/$version.mod` | The `go.mod` of that version. For a module without one, "a file containing only a module statement with the requested module path" |
  | `$base/$module/@v/$version.zip` | The module's contents (section 1.5) |
  | `$base/$module/@latest` | The same JSON as `.info`, for the latest version. "Optional, and module proxies are not required to implement it" |

- **[D]** A successful answer "must have the status code 200". 404 and 410 mean the module
  or version is "not available on the proxy, but it may be found elsewhere".
- **[S]** `web/api.go:73` maps exactly 404 and 410 to "not found"; any other non-200 answer
  is an error.
- **[S]** `web/http.go:266-277`: on a non-200 answer the go command shows the body as the
  error's detail only when `Content-Type` is `text/plain` with a `utf-8` or `us-ascii`
  charset, or no charset. It does not read `Content-Type` on a 200.

### 1.2 Case encoding

- **[D]** Module paths and versions are case-encoded in URLs: each upper-case letter is
  replaced by `!` followed by the lower-case letter, so `example.com/M` is requested as
  `example.com/!m`. This keeps paths that differ only in case apart on case-insensitive
  file systems.
- **[S]** `module.UnescapePath` and `module.UnescapeVersion` (`module/module.go:737-761`)
  reverse it. The decoder rejects an upper-case letter, a `!` not followed by a lower-case
  letter, and a trailing `!` (`unescapeString`, `module/module.go:763-792`).
- **[S]** `module.CheckPath` (`module/module.go:296-310`) admits only letters, digits and
  `-._~` in a path element, so `!` never appears in a valid module path. The first element
  "must contain at least one dot", which keeps it apart from `sumdb` (section 2).

### 1.3 The go command's requests

- **[D]** To resolve "latest" the go command requests `@v/list` and then, "if no suitable
  versions are found", `@latest`. It prefers "the semantically highest release version",
  then "the semantically highest pre-release version", then "the chronologically most
  recent pseudo-version".
- **[S]** `modfetch/proxy.go`:
  - `Versions` (lines 296-317) reads the first field of each `@v/list` line and drops lines
    that are not valid semantic versions, and pseudo-versions.
  - `Latest` (391-404) falls back to `latestFromList` (332-367) when `@latest` answers 404
    or 410. That picks, from the `@v/list` lines, the one with the newest time: a second
    field parsed as RFC 3339 if the line has one, else the time a pseudo-version encodes.
    Lines with neither are skipped. A proxy whose list leaves out pseudo-versions and that
    has no `@latest` therefore cannot resolve a module that has only pseudo-versions.
- **[S]** So a list that holds pseudo-versions changes nothing the go command resolves while
  `@latest` answers. Tested with go1.27.1 against a file proxy on 2026-10-19, with lines
  `<version> <RFC 3339 time>` that include pseudo-versions newer than every tag:
  `go list -m -versions` printed only the tagged versions; `go list -m <module>@latest` and
  `go get <module>@latest` chose the highest release, or the highest pre-release when
  there was none, and for a module with only pseudo-versions the version `@latest` named.
  - `Stat` (369-389) requests `@v/$version.info` and fails when the answer's `Version`
    differs from a requested version that is canonical and valid for the module path.
  - `GoMod` (406-420) and `Zip` (from 422) request only canonical versions, and `Zip` reads at most
    `codehost.MaxZipFile` + 1 bytes.
- **[S]** `modfetch/fetch.go:180` extracts each downloaded zip with `modzip.Unzip`, which
  checks the size limits and the `module@version/` prefix of every entry; `checkModSum`
  (line 758) checks the hash against `go.sum` and the checksum database.
- **[S]** jfrog-cli-artifactory's `go.go` `getPackageVersion` (lines 260-284) requests
  `<module>/@v/<branch>.info` for `jf go get <module>@<branch>`, and
  `<module>/@v/latest.info` when no version is given, expecting 200 and reading `Version`.
  It sends the module path as written, without case encoding.

### 1.4 Versions

- **[D]** A canonical version is a semantic version with a leading `v`, all three numbers
  and no build metadata other than `+incompatible`.
- **[D]** A pseudo-version names an untagged revision, in one of three forms:
  `vX.0.0-yyyymmddhhmmss-abcdefabcdef`, `vX.Y.Z-pre.0.yyyymmddhhmmss-abcdefabcdef` and
  `vX.Y.(Z+1)-0.yyyymmddhhmmss-abcdefabcdef`. The timestamp is the commit time in UTC.
- **[D]** `+incompatible` marks a version at major version 2 or higher of a module that has
  no `go.mod`, so its path has no `/vN` suffix.
- **[S]** `module.Check(path, version)` (`module/module.go:217-231`) checks the path, that
  the version is a valid semantic version, and that the path's major-version suffix matches
  the version; `+incompatible` is accepted only without a suffix. It does not require the
  canonical form; `module.CanonicalVersion` (line 648) gives it.
- **[S]** `module.IsPseudoVersion` and `module.PseudoVersionTime` (`module/pseudo.go:123`,
  `136`) recognise a pseudo-version and read its time.

### 1.5 Module zip files

- **[D]** A zip is at most 500 MiB, compressed or not; `go.mod` and `LICENSE` are each at
  most 16 MiB. Every entry is prefixed `$module@$version/`.
- **[S]** `zip.Create` (`zip/zip.go:523-530`) refuses a version that is not canonical and a
  pair that fails `module.Check`, and writes each entry with `zw.Create` (line 549), so the
  entries carry no modification time.

## 2. The checksum database

- **[D]** Before using a database through a proxy, the go command requests
  `<proxyURL>/sumdb/<sumdb-name>/supported`. 200 means the proxy serves the database and
  the client never connects to it directly; 404 or 410 means it connects directly. "Any
  other response is treated as the database being unavailable." Lookups and tiles then
  follow under the same prefix (research 04).
- **[D]** "GONOSUMDB must not imply GONOPROXY": a module can be fetched through a proxy and
  still be checked against the database.
- **[D]** "GOPRIVATE is a default value for GONOPROXY and GONOSUMDB". Modules matching
  GOPRIVATE or GONOSUMDB are accepted without consulting the database, and `GOSUMDB=off`
  disables it.
- **[D]** JFrog: Artifactory proxies `sum.golang.org`; `artifactory.go.sumdb.enabled=false`
  turns that off, and "you can control this feature on the client side with the GOPRIVATE
  and GONOSUMDB environment variables".
- **[D]** Research 10 cuts the sumdb proxy from v1: "clients fall back to sum.golang.org on
  404".

## 3. The JFrog CLI

### 3.1 `jf go-publish`

- **[S]** `gopublish.go` reads the deployer repository from the project's configuration,
  probes the Artifactory version and refuses one below its minimum. `publish.go`:
  - lines 146-148 require a `go.mod` in the project root;
  - the module path comes from `go.mod` and the version from the command's argument;
  - the `.mod` uploaded is the project's `go.mod` as it is;
  - `archive.go` builds the zip with `golang.org/x/mod/zip.Create` (line 146), and
    `publish.go:203-208` checks that every entry is prefixed `module@version/`;
  - lines 117-118 write the `.info` as `{"Version":v,"Time":t}` with
    `time.Now().Format("2006-01-02T15:04:05Z")`: local time followed by a literal `Z`, so
    the time is off by the client's offset from UTC;
  - build properties come from `build.CreateBuildProperties` and CI VCS properties from
    `civcs.MergeWithUserProps`;
  - the build-info artifacts it records have the path `<module>/@v/<version>.<ext>`, with
    the module path as `go.mod` writes it (line 222).
- **[S]** jfrog-client-go `artifactory/services/go/`:
  - it uploads `.zip`, then `.mod`, then `.info`, each with `PUT` to
    `<rt>/api/go/<repo>/<module>/@v/<version>.<ext>`, and stops at the first failure;
  - the `.info` is sent only when Artifactory is at least `6.10.0`
    (`ArtifactoryMinSupportedVersion`) and an info file exists;
  - neither the module path nor the version is case-encoded;
  - `CreateUrlPath` appends the properties as matrix parameters with
    `properties.ToEncodedString(true)` (`url.QueryEscape` on keys and values), and
    `addGoVersion` appends `;go.version=` + `url.QueryEscape(version)`;
  - `AddChecksumHeaders` (`artifactoryutils.go:64-70`) sends `X-Checksum-Sha1`,
    `X-Checksum-Md5`, and `X-Checksum` carrying the SHA-256;
  - each upload must answer exactly 201 (`CheckResponseStatusWithBody(..., http.StatusCreated)`);
  - it reads `X-Checksum-Sha256` from the response, and only logs when it is missing.
- **[S]** jfrog-cli `docs/buildtools/gopublish/help.go` holds two texts. `GetDescription`,
  the help `jf gp` prints, says only "Publish go package and/or its dependencies to
  Artifactory." `GetAIDescription`, a context string for AI agents that
  [jfrog-cli #3503](https://github.com/jfrog/jfrog-cli/pull/3503) added on 2026-06-08
  (`eb1d5eb7ca97`), says "The version must follow Go module versioning
  (vMAJOR.MINOR.PATCH); without 'v' prefix go tooling will reject it" and "Republishing the
  same version is rejected by Artifactory's Go repository." It gives no status code.
- **[I]** That Artifactory refuses a republish rests on that string alone. No JFrog
  documentation, KB article or Jira issue states it (item 5 of section 6).
- **[I]** Publishing the same source twice gives the same `.zip` and `.mod`, since the zip
  holds no times, and a different `.info`, since its `Time` is the moment of publishing.

### 3.2 `jf go` and `jf go-config`

- **[S]** `go.go` sets `GOPROXY` to `<base>/api/go/<repo>` followed by `|direct`, or by
  `,direct` when `FallbackOnlyIfNotFound` is set; `--no-fallback` drops `direct`. With the
  pipe the go command falls back to the module's source after any error, with the comma
  only after 404 or 410.
- **[D]** JFrog's page gives the same base,
  `https://<USERNAME>:<TOKEN>@<domain>/artifactory/api/go/<GO_REPO_NAME>`, and `jf go-config`
  as the way to write it.

## 4. Artifactory's Go repositories

### 4.1 What JFrog documents

- **[D]** "To deploy your Go module to Artifactory, use the JFrog CLI": `jf gp <GO_REPO_NAME>
  <VERSION>`.
- **[D]** "Go remote repositories can only be accessed directly through a virtual
  repository." The virtual needs a Default Deployment Repository, follows `go-import` meta
  tags by default, and its allow list defaults to `**`.
- **[D]** The page says nothing about how a version is stored, about republishing, about
  what is answered for a version deployed without `.info` or `.mod`, or about errors.
- **[D]** Research 19 lists Go among the package types direct cloud storage download covers.

### 4.2 Storage and the `go-default` layout

- **[S]** In 6.20, `GoDefaultLayoutConverter` and `artifactory.config.xml:209-213` define
  `go-default` as `[orgPath]/[module]/@v/v[refs].zip`, with
  `distinctiveDescriptorPathPattern` false and both integration-revision expressions `.*`.
- **[S]** `RepoLayoutUtils.generateRegExpFromPattern` (`RepoLayoutUtils.java:278-345`)
  turns a pattern into a regular expression. `[refs]` is neither a reserved token nor a
  custom `[name<regex>]` token, and with `failOnUnknownToken` false the parser drops it
  with a debug log. The expression then matches only a file named `v.zip` under `@v/`,
  that is no stored module file, and has no `baseRev` to read a version from.
- **[S]** 7.x replaced it. JFrog's own 7.38.8 export, a test fixture of the JFrog CLI
  (jfrog-cli-artifactory `4a0f3827cfa0`,
  `artifactory/commands/testdata/artifactory_export/etc/artifactory.config.latest.xml`,
  schema 3.1.29), defines `go-default` as
  `[orgPath]/[module]/@v/v[baseRev](-[fileItegRev]).[ext]`, both integration revisions
  `.*`, as three of the four exported descriptors in
  [research 03](03-repository-configuration-model.md#exported-7x-descriptors) do.
- **[J]** A fresh instance has the new form and one upgraded from 6.x keeps
  `v[refs].zip`: a customer reports both on 7.99.1
  ([RTFACT-31118](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-31118), open, no
  JFrog reply), and a 7.4.3 upgrade log reads "Module Info cannot be extracted from
  go-default layout - version files will not be resolved"
  ([RTFACT-22104](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-22104)). No release
  note names the change.
- **[I]** Artifactory stores a version at the path `jf gp` deploys it to,
  `<module>/@v/<version>.{zip,mod,info}`, with the module path and version as written, not
  case-encoded: the build-info `jf gp` records names that path (section 3.1).
- **[S]** artifactweave's `go-default` (`pkg/layout/bundled.go`) is 7.x's. Checked against
  `pkg/layout` on 2026-10-19 (`bundled_test.go`):
  - `github.com/go-chi/chi/v5/@v/v5.3.2.zip` gives `orgPath` `github.com/go-chi/chi`,
    `module` `v5`, `baseRev` `5.3.2` and `ext` `zip`;
  - `v1.2.0-rc.1.mod` gives `baseRev` `1.2.0` and `fileItegRev` `rc.1`, and
    `v1.0.0-alpha.beta.info` `fileItegRev` `alpha.beta`: the greedy file revision leaves
    only the last dotted part to `[ext]`;
  - a pseudo-version's timestamp and commit are its `fileItegRev`, and
    `v2.0.0+incompatible.zip` gives `baseRev` `2.0.0+incompatible`;
  - each path rebuilds from what it gives;
  - a single-element module path such as `example.com` does not match, because
    `[orgPath]/` needs a slash.

### 4.3 MIME types

- **[S]** `GoMimeTypeConverter` registers `.mod` as `text/plain+mod` and `.info` as
  `application/json+info`. Whether Artifactory sends these on a download from
  `/api/go/` is not shown.
- **[S]** artifactweave's `item.MimeType` (`internal/service/item/item.go:631-653`) maps
  `.zip` to `application/zip` and has no entry for `.mod` or `.info`, which are recorded as
  `application/octet-stream`.

## 5. artifactweave at the baseline

This section records the pre-handler state on 2026-10-16. The implemented local protocol
and index now live in `internal/server/golang` and `internal/format/golang`; decision
[0035](decisions/0035-go-modules.md) describes their contract. The observations below are
historical evidence, not the current implementation status.

- **[S]** `internal/server/stubs.go:23-25` already sends errors under `/artifactory/api/go/`
  through `apierror.SurfaceText`, which writes `text/plain; charset=utf-8` and the message
  (`apierror.go:99-103`).
- **[S]** `pkg/repo/packagetypes.go:54` gives Go the `go-default` layout.
- **[S]** `types.ComparatorFor(repo.Go)` (`pkg/types/version.go:46-58`) is `CompareSemver`.
  It trims a leading `v`, compares up to three numbers and the pre-release, and drops build
  metadata, so `+incompatible` does not affect order. It orders pseudo-versions by their
  pre-release text, not by the time they encode.
- **[S]** Matrix parameter values are decoded with `url.QueryUnescape` since #260, so the
  properties `jf gp` encodes arrive intact; a literal `+` in a path segment, as in
  `v2.0.0+incompatible.zip`, is kept.
- **[S]** The generic deploy (`internal/server/items.go`, `deployItem`) needs Delete to
  overwrite a file with other content (the same content, declared by its SHA-1 or SHA-256,
  needs Deploy only, as on 7.x; #400), patches the request's properties into the stored ones, and answers 201 with the
  item JSON but without `X-Checksum-*` headers. It reads `X-Checksum` by its length, so the
  SHA-256 `jf` sends there is checked.
- **[S]** No Go handler exists: `format.For` (`internal/format/format.go:35-40`) answers the
  generic handler for Go, which keeps no package index.

## 6. Inferred and undocumented

These go to the live-capture list ([#216](https://github.com/weaveplatform/artifactweave/issues/216)):

1. The case form Artifactory stores a module path and version in: as `jf gp` sends them, or
   case-encoded (section 4.2).
2. Which properties Artifactory sets on the stored files beyond those `jf gp` sends, such
   as `go.version`.
3. How Artifactory composes `@v/list` and `@latest`: the order of the lines, the form of
   each line's time, the time of a version without a `.info`, and whether a version without
   `.mod`, `.zip` or `.info` is listed. Section 8 settles that pseudo-versions are listed
   with their `.info` time.
4. What Artifactory answers for `.info` and `.mod` of a version deployed without them.
5. The status and body when a version is republished, with identical and with different
   content, for each of `.zip`, `.mod` and `.info`, and whether the generic deploy, copy
   and move refuse it too (section 3.1).
6. The status, `Content-Type` and body for an unknown module, an unknown version, a
   repository of another package type, and a remote and a virtual Go repository requested
   directly.
7. Whether `X-Checksum-Sha256` and the other `X-Checksum-*` headers are sent on a deploy
   through `/api/go/`, and on a generic deploy.
8. Whether a fresh 7.161 instance's `go-default` is still the 7.38.8 export's, and whether
   its layout-based searches resolve Go versions (section 4.2).
9. Whether `@v/list` and `@latest` leave out versions the caller cannot read.
10. The `Content-Type` of `.mod`, `.info` and `@v/list` downloads (section 4.3).

## 7. Versions at the research baseline

| Component | Version |
|---|---|
| Go Modules Reference | <https://go.dev/ref/mod>, read 2026-10-16 |
| go command | go1.27.1 |
| `golang.org/x/mod` | v0.42.0 |
| jf | 2.126.0 |
| jfrog-cli-artifactory | `4a0f3827cfa0` |
| jfrog-client-go | `0646d7b50f9a` |
| jfrog-cli | `eb1d5eb7ca97` |
| Artifactory OSS | 6.20 (`knottnhupfer/artifactory-oss` `b99e5649bcad`) |

## 8. Artifactory 7.x check

Checked against Artifactory 7.161.32 on 2026-10-17 ([#341](https://github.com/weaveplatform/artifactweave/issues/341)); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

**Sources.** JFrog's current documentation, read on 2026-10-17 (the pages named below), and the Artifactory release notes: self-managed 7.84.3 to 7.161.32 ([self-managed releases](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases)), SaaS 7.80.0 to 7.174.0 ([SaaS releases](https://docs.jfrog.com/releases/docs/artifactory-saas-releases)), the [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues) and the [deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations). JFrog no longer publishes the notes for 7.0 to 7.83 (the [end-of-life page](https://docs.jfrog.com/releases/docs/artifactory-end-of-life) keeps only their dates), so a change in that range shows only through the current documentation. The Go pages read are [go-modules](https://docs.jfrog.com/artifactory/docs/go-modules.md) and [jf-go](https://docs.jfrog.com/artifactory/docs/jf-go.md).

**Confirmed for 7.x** (go-modules):
- The go command cannot deploy, and `jf gp` publishes.
- Go modules resolve only from local or virtual repositories, and a remote serves through a
  virtual repository.
- Artifactory proxies `sum.golang.org`, switched by `artifactory.go.sumdb.enabled` and
  `artifactory.go.sumdb.url.override`.

**Changed or added in 7.x:** every Go note from 7.84 to 7.161 concerns remote repositories and
their VCS providers (P9). They are:
- GitHub, GitHub Enterprise Server ([7.98.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7987-self-managed)) and GitHub Enterprise Cloud
  ([7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed));
- GitLab subgroups ([7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed), [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed));
- Bitbucket tags with slashes ([7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed));
- major versions v2 and above through a virtual repository ([7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)), and
  `+incompatible` submodules ([7.146.12](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-714612-self-managed));
- `.zip` checksums for Go 1.24's vendor folder aligned between the registry and the proxy
  ([7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed));
- include and exclude patterns honoured for a remote's `.info` files ([7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed)).

None changes what a local repository stores or answers. Jira and JFrog's 7.x export show
two earlier changes that do ([#378](https://github.com/weaveplatform/artifactweave/issues/378)):
- A local repository's `@v/list` gives each version a second field, its time from the
  version's `.info`, and lists the pseudo-versions `jf gp` pushed ([J]
  [RTFACT-21670](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-21670), fixed in 7.8.1:
  Artifactory "is using the last update timestamp for local Go repositories when populating
  the version list ... we should use that information"; JFrog's QA marked it resolved after
  testing on 7.x). The go command picks the same versions with such a list (section 1.3),
  and decision 0035 lists them so.
- `go-default` is `[orgPath]/[module]/@v/v[baseRev](-[fileItegRev]).[ext]` on a fresh 7.x
  instance (section 4.2), and `pkg/layout` bundles it.

**Unconfirmed for 7.x:** Artifactory's Go handler was closed source before 7, so the 6.20 mirror
holds nothing for this report beyond the shared repository code. Every **[I]** above stays on
[#216](https://github.com/weaveplatform/artifactweave/issues/216).
