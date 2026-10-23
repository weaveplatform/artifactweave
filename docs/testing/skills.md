# Skills local publish and index qualification

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
[#689](https://github.com/weaveplatform/artifactweave/issues/689) owns
ClawHub discovery, reads, download, search, scan status and pinned
`jf agent skills` install/publish qualification. Until that lands, a
successful publish does not imply a skill can be installed through the CLI.
The exact 7.161.32 publish status/body and overwrite details remain
unobserved [I] on [#216](https://github.com/weaveplatform/artifactweave/issues/216).

- [JFrog Skills repository guide](https://docs.jfrog.com/artifactory/docs/skills-repositories)
