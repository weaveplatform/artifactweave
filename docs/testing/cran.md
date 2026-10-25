# CRAN repository client and qualification

Phase 9c issue [#778](https://github.com/weaveplatform/artifactweave/issues/778)
implements R package resolution for local, remote, virtual and one-node
federated CRAN repositories. The protocol contracts follow the current
[JFrog CRAN guide](https://docs.jfrog.com/artifactory/docs/cran-repositories),
[JFrog reindex API](https://docs.jfrog.com/artifactory/reference/calculatecranrepositorymetadata),
and the [R Installation and Administration manual](https://cran.r-project.org/doc/manuals/r-release/R-admin.html).

## Connect R

Create a repository with `packageType: cran`. The base URL for R is
`https://<host>/artifactory/<repo>`; use this URL as `repos` in
`install.packages()` or `available.packages()`. R reads `src/contrib/PACKAGES`
or `src/contrib/PACKAGES.gz` and then the advertised source archive. Its
optional `PACKAGES.rds` request receives 404, so R falls back to the text
index. Windows binaries use `bin/windows/contrib/<R-major.minor>` and macOS
binaries use `bin/macosx/contrib/<R-major.minor>` (or its architecture subpath).
The Linux binary path is `bin/linux/contrib/<R-major.minor>`.

Publish source archives by direct `PUT` to
`src/contrib/<name>_<version>.tar.gz` (also `.tar.bz2` and `.tar.xz`) or by
raw archive `POST /artifactory/api/cran/<repo>/sources`. Publish Windows
`.zip`, macOS `.tgz`, or Linux `.tar.gz` binaries by direct `PUT` under their
terminal feed directory. Raw binary
`POST /artifactory/api/cran/<repo>/binaries?distribution=<distribution>&rVersion=<major.minor>`
infers package identity and destination from the archive; accepted
distributions are `windows`, `mac` or `macosx`, and `linux`. A virtual repo
needs a local default deployment repository. Both upload forms validate the
full archive, paths, and `DESCRIPTION` identity before publishing it.

Local and federated writes, deletes, and trash restores refresh derived
`PACKAGES` and `PACKAGES.gz`; the index advertises the highest R package
version in each terminal feed. `POST /artifactory/api/cran/reindex/<repo>`
rebuilds a local repo synchronously by default or queues it with `async=true`.
Remote reads validate upstream metadata and package content before caching,
and cached content is reusable offline. Virtual indexes merge members in
order and omit packages the caller cannot read from both the member and the
virtual repository. Archive files under `src/contrib/Archive/<name>/` remain
addressable without appearing in the latest version index.

The guide also documents a feature-flagged `GET /api/cran/<repo>/move-archives`
for relocating archives written in legacy layouts. That storage migration
needs its own Phase 9c ticket; this implementation does not migrate existing
legacy paths. Cross-peer federation belongs to P13.

## Evidence

[`@cran` acceptance](../../test/acceptance/features/cran.feature) uses
`r-base:4.5.2` pinned by digest and the unchanged official CRAN
`assertthat_0.2.1.tar.gz` archive. A real `Rscript` calls
`install.packages()` through local, one-node federated, remote and virtual
repositories, then repeats remote installation after the upstream stops.
The six focused scenarios passed all 66 steps locally on 2026-10-25; the
remote archive redirect was followed and verified on the upstream fixture.
The source fixture and independent published `PACKAGES` entry remain unchanged
when a server validation defect is found. Focused Go tests use the unchanged
CRAN Windows R 4.5 ZIP to verify binary indexing, and cover source and binary
POST uploads, delete/restore/reindex, malformed archives, unsafe metadata,
and permission-filtered virtual indexes.
