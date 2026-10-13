# 0003: Repository model and Docker access method

Status: Proposed

## Context

Artifactory's repository is the unit everything else hangs off: a key, a class (`rclass`:
local, remote, virtual, federated), a package type, a layout, include and exclude patterns
and a long tail of per-type settings. The JSON configuration is what the Terraform
providers, the JFrog CLI and the YAML descriptor all speak, with typing quirks (some
booleans are strings on local repositories) and keys that exist only in the Terraform
provider source.

Docker clients need a URL layout. Artifactory offers three (repository path, subdomain and
port per repository), each needing reverse-proxy or DNS support.

## Decision

**One configuration struct.** `pkg/repo.Config` mirrors the Artifactory repository JSON
in [03-repository-configuration-model](../03-repository-configuration-model.md): the
common fields, the local, remote, virtual and federated fields, the per-package-type
fields, the enumerations, the defaults, the string-typed booleans per class and the
provider-only keys. Validation enforces the allowed package types per class, the layout
reference, the include and exclude patterns and the per-type constraints. The struct is
importable because other weave repositories will declare repositories as code.

**Classes.** `local`, `remote`, `virtual` and `federated` are implemented. A federated
repository is a local repository whose members are paired artifactweave peers, kept in
step by the peer protocol ([0022](0022-federated-repositories.md)). The earlier shim that
treated `federated` as local and rejected multi-member lists was withdrawn on 2026-10-13
when federation came into scope.

**Package types in scope.** `docker`, `oci`, `helm`, `helmoci`, `npm`, `maven`, `gradle`,
`go`, `pypi`, `nuget`, `terraform`, `generic`, and `releasebundles` as the storing
repository type for release bundles v1 ([0023](0023-release-bundles-and-distribution.md)). Other types are rejected with the
Artifactory envelope and a message naming the type.

**Package-type handlers.** Each type registers capabilities with the repository service:
its layout, its metadata generation (what to rebuild on write and delete), its remote
adapter, its version comparator for the package index and its reindex endpoint.

**Docker access method.** Repository-path routing is native and the default:
`/v2/{repo}/{image...}` is routed to the Docker handler for `{repo}` without any reverse
proxy, which is what `docker push host/<repo>/<image>` sends and what the OCI conformance
suite needs. Subdomain matching on the `Host` header is a configuration option. Port per
repository is out of scope. `/v2/_catalog` lists across repositories. A repository key must
not collide with the first path segment of another repository's images; the validator
rejects such keys.

## Rationale

A single struct that is byte-compatible with the wire format removes a conversion layer
and lets the same validation serve the REST API, the YAML descriptor and future Go
consumers. Faithful reproduction of the typing quirks is cheaper than fighting every client
that round-trips them.

Repository-path routing is the only Docker method that works on one port with no
infrastructure, so it is the one a single static binary must own.

**Alternatives considered:**
- *A clean internal model with converters*: more code, and every provider-only key would
  need tracing through two layers.
- *Rejecting `federated`*: breaks Terraform modules that already use it, for no gain.
- *Accepting `federated` as a local-only shim*: the first version of this decision; it
  kept single-member plans working but could not honour real member lists.
- *Subdomain as default*: needs wildcard DNS and certificates before the first push works.

## Constraints

- Keys and types that Artifactory accepts but artifactweave does not implement must fail
  loudly at create time, never at first use.
- `terraformbackend` is out of v1 scope and is rejected at create time.
- The repository-path method means image names cannot start with a segment equal to
  another repository key; this is documented in the operator guide.

## Verification

- `pkg/repo` round-trip tests for every field in the research tables, including string
  and boolean forms per class.
- Validation tests for the package-type sets per class and for key collisions.
- Terraform acceptance: every in-scope `artifactory_<class>_<type>_repository` resource
  applies, plans clean and destroys.
- OCI conformance against `/v2/<repo>/<image>`.

## References

- [03-repository-configuration-model](../03-repository-configuration-model.md)
- [04-package-protocols](../04-package-protocols.md), Docker section
- [08-terraform-providers](../08-terraform-providers.md)
- <https://docs.jfrog.com/integrations/docs/configuration-json-files>
