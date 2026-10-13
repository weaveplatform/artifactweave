# 0018: Package index

Status: Proposed

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
versioning for npm, Go, Helm and Terraform; Maven version ordering; PEP 440 for PyPI;
NuGet SemVer2; manifest timestamps for Docker tags.

Retention, search and the registration and version endpoints read the index. The item
tree remains the source of truth; a reindex rebuilds the index from it.

## Rationale

Without a shared index each format would grow its own path-globbing version logic and
cleanup policies could not be correct across types. A derived index that can always be
rebuilt keeps the item tree authoritative.

**Alternatives considered:**
- *Computing versions on demand from paths*: quadratic for large repositories and wrong
  for formats whose version is inside metadata (npm, NuGet).

## Constraints

- The index is eventually consistent with the item tree during a reindex; reads during a
  rebuild may be partial.

## Verification

- Comparator tests per package type against documented ordering examples.
- Index maintenance tests for every write path per format.
- Reindex rebuild equivalence test.

## References

- [04-package-protocols](../04-package-protocols.md)
- [07-webhooks-retention-projects](../07-webhooks-retention-projects.md), retention criteria
- <https://peps.python.org/pep-0440/>
- <https://learn.microsoft.com/en-us/nuget/concepts/package-versioning>
