# 0018: Package index

Status: Proposed

Checked against: **Artifactory 7.161.32** on 2026-10-17 ([research 24 section 6](../24-package-format-handlers.md#6-artifactory-7x-check), [#341](https://github.com/weaveplatform/artifactweave/issues/341)).

## Context

Many features need "package X, version Y, files [...]" rather than a path tree: cleanup
policies (`keepLastNVersions`, `includedPackageVersions`), `maxUniqueSnapshots`,
`maxUniqueTags` and Docker tag retention, npm search, NuGet registration pages and
version lists, Terraform version lists, the `GET /api/v2/repositories` package view.
Each package type orders versions differently.

## Decision

The store keeps a `packages`, `versions` and `version_files` index. Each format handler
maintains it on write, delete, move, copy and reindex, recording the package name, the
version string, the parsed version for ordering, timestamps, download statistics and the
files that make up the version. Each package type registers a comparator: semantic
versioning for npm, Go, Helm and Terraform; Artifactory's Mercury ordering for Maven,
Gradle, Ivy and SBT ([0036](0036-maven.md)); PEP 440 for PyPI;
NuGet SemVer2; manifest timestamps for Docker tags.

Retention, search and the registration and version endpoints read the index. The item
tree remains the source of truth; a reindex rebuilds the index from it.

**Docker, OCI and Helm OCI images** ([0031](0031-docker-oci-registry.md),
[research 23](../23-docker-oci-registry.md) section 8):
- **Package:** an image, keyed by its repository and `<name>`, present while the image
  holds any manifest, tagged or not.
- **Version:** a tag, the name of a tag folder that holds `manifest.json` or
  `list.manifest.json`. Digest folders (`sha256__<hex>`) are not versions; they are the
  manifests that tags and indexes point at.
- **Files:** the tag folder's items: the manifest, the config and the layers.
- **Order:** the tag's last push time. Overwriting a tag moves it to the newest place.
- **Retention:** `maxUniqueTags` and `dockerTagRetention` run after each push, on the
  pushed image only, with the rules in decision 0031; a tag removed by `maxUniqueTags`
  takes its retained versions with it.
- **Reads:** the tags list and the catalogs read the index, not the folder tree.

**npm, Go and Maven** (P7) maintain the index through the format hooks of decision
[0032](0032-package-format-handlers.md), on every write path including restore from the
trash can and items cached from a remote. Each format's decision sets its package, version,
files and order: npm ([0034](0034-npm.md)), Go modules ([0035](0035-go-modules.md)), and
Maven with Gradle, Ivy and SBT ([0036](0036-maven.md)). For all of them:
- **A remote repository's index** covers its cache, which alone holds a remote's items
  (`pkgindex.Stored`, #361). The hooks and a reindex both read only there, and the
  contract suite's cache scenario holds every format to it. Docker's cache follows with
  its remote repositories in P9.
- **Per-version metadata** (`PackageVersion.Metadata`, such as an npm packument fragment,
  or the time a Go version's `.info` gives, which `@v/list` answers, #417) is a cache of
  what the version's items, content and properties say. A reindex rebuilds
  it, and nothing is kept there that the items do not carry.
- **Download statistics:** a download served through the item path records the version's
  last download (`RecordVersionDownload`), as it records the item's.
- **Derived metadata and retention** that would contend in the write transaction run from
  the persistent work queue ([0033](0033-persistent-work-queue.md)): Maven metadata and
  `maxUniqueSnapshots`.

## Rationale

Without a shared index each format would grow its own path-globbing version logic and
cleanup policies could not be correct across types. A derived index that can always be
rebuilt keeps the item tree authoritative.

**Alternatives considered:**
- *Computing versions on demand from paths*: quadratic for large repositories and wrong
  for formats whose version is inside metadata (npm, NuGet).

## Artifactory 7.x check

The decision stands against 7.161.32. The package index is artifactweave's own design, and no
7.x page or note describes Artifactory's internal one. The 7.x notes in this area cover npm and
Maven indexing performance and the following:
- **Docker download statistics.** [7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed) can share download statistics across tags of
  one digest (an opt-in setting). This touches the version download statistics here
  (`pkgindex.RecordDownload`). It is an input for when Docker adopts that setting.

## Constraints

- The index is eventually consistent with the item tree during a reindex; reads during a
  rebuild may be partial.

## Verification

- Comparator tests per package type against documented ordering examples.
- Index maintenance tests for every write path per format.
- Reindex rebuild equivalence test.
- Docker: tag order after overwrites, `maxUniqueTags` and `dockerTagRetention` after a push,
  and a multi-architecture tag removed as one unit.
- npm, Go and Maven: the shared format contract suite (`internal/format/formattest`, #268)
  on the in-memory, SQLite and PostgreSQL stores.

## References

- [04-package-protocols](../04-package-protocols.md)
- [07-webhooks-retention-projects](../07-webhooks-retention-projects.md), retention criteria
- <https://peps.python.org/pep-0440/>
- <https://learn.microsoft.com/en-us/nuget/concepts/package-versioning>
