# 11: Prior art

Checked against: Artifactory 7.161.32

Existing registries that solve parts of the same problem, what artifactweave borrows from
each, and what it deliberately does not copy. The comparison is limited to projects whose
behaviour or source was checked at the research baseline.

Research baseline: **2026-10-12**. Claims about licences and format coverage cite the
project's own documentation; design observations come from reading the sources. Items
marked *(unverified)* were not confirmed against a primary source.

## Summary

| Project | Language, licence | Formats | Repository model | Borrow |
|---|---|---|---|---|
| Gitea package registry | Go, MIT | container, npm, Maven, PyPI, NuGet, Helm, Go, Terraform and others | local only, per owner | handler layout per format, metadata generation, version comparators, test fixtures |
| Athens | Go, MIT | Go modules only | proxy and cache | GOPROXY semantics, upstream fallback and `GONOSUMDB` handling |
| CNCF distribution | Go, Apache-2.0 | OCI only | local, with proxy cache mode | blob upload session model, digest verification, storage driver interface |
| zot | Go, Apache-2.0 | OCI only | local, sync from upstreams | referrers handling, conformance-suite usage, sync policy |
| Harbor | Go, Apache-2.0 | OCI, plus proxy cache | local and proxy projects | proxy-cache semantics, tag retention policy engine |
| Sonatype Nexus Repository | Java, EPL-1.0 (OSS edition) | 18 or more formats | hosted, proxy, group | the hosted, proxy, group model (the same shape as local, remote, virtual) |
| Pulp | Python, GPL-2.0 | many, through plugins | remote, repository, distribution | nothing structural; a reference for sync and publish semantics |
| Cloudsmith | SaaS | many | hosted and upstreams | nothing; a reference for upstream chaining |

## Gitea package registry

Gitea's package registry is a Go implementation of local repositories for Alpine, Cargo,
Chef, Composer, Conan, Conda, Container, CRAN, Debian, Generic, Go, Helm, Maven, npm, NuGet,
Pub, PyPI, RPM, RubyGems, Swift, Vagrant and Terraform
(https://docs.gitea.com/usage/packages/overview/). Each format lives under
`routers/api/packages/<format>` with a thin router and handlers that parse the client
protocol, plus a `modules/packages/<format>` package that parses the uploaded file's
metadata. The licence is MIT, so code may be studied and adapted with attribution.

Borrow:

- The per-format split between protocol handlers and metadata parsers; artifactweave's
  `internal/format/<name>` follows the same shape with a shared package index.
- The NuGet implementation (service index, flat container, registration pages, search,
  V2 OData subset) and the Maven snapshot metadata generation, both of which are known to
  work against the real clients.
- Gitea's version comparators per format and its handling of normalised names (PyPI PEP
  503, npm scopes, NuGet lower-casing).
- Its integration tests, which drive the real clients in CI, as a model for the acceptance
  features.

Do not copy: Gitea has no remote or virtual repositories, no Artifactory-shaped management
API, no properties, no AQL, and its ownership model (packages belong to a user or
organisation) does not map to repository keys. Terraform support is recent and only covers
modules *(unverified whether providers are supported)*.

## Athens

Athens (https://github.com/gomods/athens) is the reference Go module proxy: GOPROXY
protocol, upstream fallback to `proxy.golang.org` or VCS, storage backends (disk, S3, GCS,
and others), and a filter file for allow and deny rules. Licence MIT.

Borrow: the exact GOPROXY semantics (`@v/list`, `.info`, `.mod`, `.zip`, `@latest`, the
`!x` case encoding, 404 and 410 as "try the next proxy"), the behaviour when a module is
missing upstream, and the decision to not proxy sumdb by default.

Do not copy: Athens stores modules by module path and version, not as artifacts with
properties and checksums, and has no concept of a local repository that accepts publishes.

## CNCF distribution and zot

`distribution/distribution` (https://github.com/distribution/distribution) is the original
Docker registry: the blob upload session model (`POST`, `PATCH`, `PUT` with digest), the
storage driver interface, the proxy-cache mode and the error code vocabulary. It had no
referrers API at the research baseline. zot (https://github.com/project-zot/zot) is a
pure OCI registry with referrers, sync from upstream registries, and it is validated with
the official conformance suite. Both are Apache-2.0.

Borrow: the upload session design for `pkg/blob` (begin, append, commit with digest,
abort), the digest verification rules, the error codes, zot's referrers behaviour and its
use of the conformance suite as a CI job, and zot's sync policies as a model for the remote
Docker repository's cache rules.

Do not copy: neither models the Artifactory storage layout
(`<repo>/<image>/<tag>/manifest.json` with layers beside it), the `_catalog` extension in
zot's case, or token issuance in the Artifactory realm form.

## Harbor

Harbor (https://github.com/goharbor/harbor) adds proxy-cache projects, tag retention
policies, replication between registries and a permission model on top of distribution.
Apache-2.0.

Borrow: the proxy-cache semantics (manifest requests are checked upstream while blobs are
served from cache), the retention policy engine's rule shape (keep the last N, keep by
age, by tag pattern) as a cross-check for cleanup policies, and the replication job model.

Do not copy: Harbor is a container-only platform with its own API and UI.

## Sonatype Nexus Repository

Nexus (https://github.com/sonatype/nexus-public) is the closest universal repository
manager: hosted, proxy and group repositories across 18 or more formats, a REST API and a
scripting API. The OSS edition is EPL-1.0 and written in Java.

Borrow: only the repository model vocabulary, which matches Artifactory's local, remote and
virtual, and its documented behaviour for group repository metadata merging as a second
reference when JFrog's documentation is silent.

Do not copy: Java source under EPL, and an API that is incompatible with the JFrog clients.

## Pulp and Cloudsmith

Pulp (https://pulpproject.org, GPL-2.0, Python) separates remotes, repositories, repository
versions and distributions, and is useful as a reference for how sync and publish are
modelled as tasks with versions. Cloudsmith is a SaaS service with upstream chaining per
format and no source. Neither contributes code; both document behaviours (immutable
repository versions, upstream priority ordering) that inform the remote and virtual
decision records.

## What none of them provide

The Artifactory wire protocol: the repository configuration JSON, the storage API and its
matrix parameters, AQL, properties, checksum deploy, the Access identity APIs, the YAML
descriptor, the JFrog CLI expectations and the Terraform provider expectations. That
surface is the reason artifactweave exists and is specified in documents 02 through 09.

Primary URLs: https://docs.gitea.com/usage/packages/overview/,
https://github.com/go-gitea/gitea/tree/main/routers/api/packages,
https://github.com/gomods/athens, https://github.com/distribution/distribution,
https://github.com/project-zot/zot, https://github.com/goharbor/harbor,
https://github.com/sonatype/nexus-public, https://pulpproject.org,
https://cloudsmith.com.
