# Skills local publish, discovery and client qualification

Issue [#688](https://github.com/weaveplatform/artifactweave/issues/688)
implements the local Skills repository's documented
`POST /artifactory/api/skills/{repo}/api/v1/skills` multipart publish
operation. The request carries a JSON `payload` with a lowercase
hyphenated slug and SemVer version, plus one or more `files[]` parts.
The server stores a reproducible ZIP at
`{slug}/{version}/{slug}-{version}.zip`. Path identity and the ZIP's
SHA-256 fingerprint are authoritative. `SKILL.md` at the root or in a
subdirectory can add `skill.*` metadata; a bundle without it is still
indexable. Publishing an existing version requires delete permission in
addition to write permission.

The parser never extracts the ZIP to disk. It rejects absolute, traversing,
duplicate and symlink entries, corrupt content and excessive sizes before
indexing. The current service limits a request to 256 files, 32 MiB of
archive content, 5 MiB for `SKILL.md`, and bounded metadata fields. These
are artifactweave admission limits, not measured Artifactory limits. All
item-service writes, copies and restores run the same format hook; delete
prunes the package index, and a full reindex rebuilds it from stored ZIPs.
Versions sort by SemVer.

`go test ./internal/format/skills ./internal/server/skills ./pkg/types`
checks multipart success and refusal, ZIP/path safety, metadata, package
properties, delete/restore/copy, reindex and version ordering. Issue
[#689](https://github.com/weaveplatform/artifactweave/issues/689) adds
ClawHub discovery, permission-filtered list/detail/versions/search, fingerprint
resolve, bundle and single-file reads, and scan-status responses. Only the
well-known discovery route allows anonymous access. The pinned JFrog CLI
2.126.0 exercises publish, list, search, install, update and delete in the
[`@skills` native scenario](../../test/acceptance/features/skills.feature).
Real Xray scanning remains P26; unsigned install uses the documented client
option until P23 implements signed Evidence verification.
The exact 7.161.32 publish status/body and overwrite details remain
unobserved [I] on [#216](https://github.com/weaveplatform/artifactweave/issues/216).

- [JFrog Skills repository guide](https://docs.jfrog.com/artifactory/docs/skills-repositories)
