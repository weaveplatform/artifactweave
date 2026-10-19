# 0003: Repository model and Docker access method

Status: Proposed

Checked against: **Artifactory 7.161.32** on 2026-10-17 ([research 03](../03-repository-configuration-model.md#artifactory-7x-check), [#341](https://github.com/weaveplatform/artifactweave/issues/341)); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

## Context

Artifactory's repository is the unit everything else hangs off: a key, a class (`rclass`:
local, remote, virtual, federated), a package type, a layout, include and exclude patterns
and a long tail of per-type settings. The JSON configuration is what the Terraform
providers, the JFrog CLI and the YAML descriptor all speak, with typing quirks (JFrog
documents some booleans as strings, while Artifactory sends booleans) and keys that exist only in the Terraform
provider source.

Docker clients need a URL layout. Artifactory offers three (repository path, subdomain and
port per repository), each needing reverse-proxy or DNS support.

## Decision

**One configuration struct.** `pkg/repo.Config` mirrors the Artifactory repository JSON
in [03-repository-configuration-model](../03-repository-configuration-model.md): the
common fields, the local, remote, virtual and federated fields, the per-package-type
fields, the enumerations, the defaults, the booleans JFrog documents as strings (accepted
in either spelling, written as booleans) and the provider-only keys. Validation
(`repo.Validate`) checks the key, the class, the package types allowed for the class, the
fields scoped to a class (`terraformType`, `proxy`), a remote's `url`, that a virtual's
default deployment repository is one of its members, and that counts and periods are not
negative. It does not check `repoLayoutRef` against the known layouts, the include and
exclude patterns or the enumerated values. A virtual repository may have no members;
`repos.CheckMembers` checks the members against the stored repositories: each must exist
and have the virtual repository's package type unless it is generic, with the Maven
family counted as one type ([research 03](../03-repository-configuration-model.md),
[#394](https://github.com/weaveplatform/artifactweave/issues/394)). The struct is
importable because other weave repositories will declare repositories as code.

**Classes.** `local`, `remote`, `virtual` and `federated` are implemented. A federated
repository is a local repository whose members are paired artifactweave peers, kept in
step by the peer protocol ([0022](0022-federated-repositories.md)). The earlier shim that
treated `federated` as local and rejected multi-member lists was withdrawn on 2026-10-13
when federation came into scope.

**Package types in scope.** `docker`, `oci`, `helm`, `helmoci`, `npm`, `maven`, `gradle`,
`go`, `pypi`, `nuget`, `terraform`, `generic`, and `releasebundles` as the storing
repository type for release bundles v1 ([0023](0023-release-bundles-and-distribution.md)). Other types the class allows are
accepted, and their configuration is persisted and echoed with no package handler of its
own. A type the class does not allow is refused with the Artifactory envelope and a
message naming the type.

**Package-type handlers.** Each type registers capabilities with the repository service:
its layout, its metadata generation (what to rebuild on write and delete), its remote
adapter, its version comparator for the package index and its reindex endpoint.

**Docker access method.** Repository-path routing is native and the default:
`/v2/{repo}/{image...}` is routed to the Docker handler for `{repo}` without any reverse
proxy, which is what `docker push host/<repo>/<image>` sends and what the OCI conformance
suite needs. Port per repository is out of scope. The registry answers on these paths
(decision [0031](0031-docker-oci-registry.md) has what each operation does):

| Path | Serves |
|---|---|
| `/v2/` and `/artifactory/api/docker/{repo}/v2/` | the version probe |
| `/v2/token` and `/artifactory/api/docker/{repo or null}/v2/token` | the registry token ([0005](0005-authentication-and-tokens.md)) |
| `/artifactory/api/docker/{repo}/v2/auth` and `.../v1/auth` | the `config.json` entry |
| `/v2/_catalog` | the images of every Docker repository the caller can read, each as `<repo>/<image>` |
| `/v2/{repo}/_catalog` and `/artifactory/api/docker/{repo}/v2/_catalog` | the images of one repository, as `<image>` |
| `/v2/{repo}/{name}/...` and `/artifactory/api/docker/{repo}/v2/{name}/...` | the registry operations on `{name}` |

`POST /artifactory/api/docker/{repo}/v2/promote`, `POST .../v2/parentManifests` and
`DELETE .../v2/delete` are JFrog REST operations and stay with the generated server. `_catalog` and `tags/list` stay in the
OpenAPI sources for documentation, marked `x-handwritten: true`, because the registry
handler serves them.

- **Repository lookup.** The key segment is matched without regard to case, as keys are
  unique, because Docker clients send references in lower case only. A key that is no
  repository answers 404 `NAME_UNKNOWN`, and so does a repository whose package type is not
  `docker`, `oci` or `helmoci`.
- **No key collisions.** On the repository-path method the first segment is always the
  key, so no image name can be taken for another repository's key, and the registry-wide
  paths (`/v2/token`, `/v2/_catalog`) have fewer segments than any repository operation.
  The registry reserves no key for its own paths. A Docker, OCI or Helm OCI key has no
  upper case letters and no `_`, as 7.x requires of every key of those types
  ([research 03](../03-repository-configuration-model.md#key-rules)). Keys Artifactory
  accepts but a client cannot send (a leading `-` or `.`, a trailing separator, a
  non-ASCII letter) are accepted as Artifactory accepts them and are documented as
  unreachable from Docker clients.
- **Subdomain option.** `docker.subdomain_match: true` adds the subdomain method beside
  the repository path, and needs `base_url`, whose host is the parent domain; the server
  refuses to start without it. The addressed host is the first `X-Forwarded-Host` value,
  else `Host`, without its port and in lower case. When it is `<label>.<base_url host>`
  and `<label>` names a Docker, OCI or Helm OCI repository, the request is for that
  repository: `/v2/{name}/...` carries no key, `/v2/_catalog` lists that repository and the
  token realm names it. Any other host is the repository-path method. Reads of
  `dockerReverseProxyMethod` answer `SUBDOMAIN` when the option is on and `REPOPATHPREFIX`
  otherwise, with `serverName` the `base_url` host.
- **Remote and virtual Docker repositories** answer every registry operation with 405
  `UNSUPPORTED` and a message naming the class, until upstream fetching and member
  resolution exist ([0007](0007-remote-repositories.md), [0008](0008-virtual-repositories.md)).
  The version probe, the token and the `auth` endpoints work for them, so logging in to a
  virtual repository already works.
- **Remote and virtual npm and Go repositories** answer their protocol with 501 in the
  format's own envelope (npm's `{"error":...}`, Go's plain text), naming the class, until
  P9 and P10 bring upstream fetching and member resolution ([0032](0032-package-format-handlers.md)).
  Maven is served by the item routes and keeps their behaviour: a remote refuses deploys and
  a virtual deploys to its default deployment repository.

**Repositories API** (research 02). The behaviour where JFrog leaves room:
- **Keys:** unique without regard to case, as Artifactory enforces them, and held to 7.x's
  key rules: a length for each class, no white space or special characters, no reserved
  name or prefix, no `-cache` suffix, and lower case without `_` for Docker, OCI and Helm
  OCI ([research 03](../03-repository-configuration-model.md#key-rules)). The rules apply to
  every key a client chooses, on create, batch create, the configuration descriptor and an
  update that spells the key in another case. An update that keeps the stored key does not
  check it again, so a repository the server creates under a reserved name, such as
  `artifactory-build-info`, is not refused for its name. A key may start with a digit, as
  7.x has allowed since 7.53.1.
- **Updates:** an update merges into the stored configuration. A field present replaces
  the stored value, a field left out keeps it, `null` resets it to its default, and `key`,
  `rclass` and `packageType` cannot change.
- **Secrets:** reads mask the remote password and sensitive header values for everyone,
  and an update that leaves the password out or sends a masked header back keeps the
  stored secret.
- **Non-admin reads:** callers other than administrators see the repositories a
  permission lets them read, holding READ on a target that covers the repository, and
  receive the public subset of its configuration. Path patterns are evaluated in P4.
- **v2 read:** drops the package-specific field groups that do not apply to the
  repository's package type.
- **Deletes:** a delete removes the configuration, the content (for a remote, its cache
  too), the package index and the repository's place in virtual repositories, in one
  transaction. Blob references are released for garbage collection. Until the trash can
  lands (#48) the content does not pass through it.
- **Protected keys:** the trash can, support bundle and `artifactory-build-info`
  repositories cannot be deleted.

## Rationale

A single struct that is byte-compatible with the wire format removes a conversion layer
and lets the same validation serve the REST API, the YAML descriptor and future Go
consumers. Following the types Artifactory actually sends, rather than the documented ones,
is cheaper than fighting every client that round-trips them: the Terraform provider decodes
the flags documented as strings into booleans, and its refresh fails on a string (#109).

Repository-path routing is the only Docker method that works on one port with no
infrastructure, so it is the one a single static binary must own.

**Alternatives considered:**
- *A clean internal model with converters*: more code, and every provider-only key would
  need tracing through two layers.
- *Rejecting `federated`*: breaks Terraform modules that already use it, for no gain.
- *Accepting `federated` as a local-only shim*: the first version of this decision; it
  kept single-member plans working but could not honour real member lists.
- *Subdomain as default*: needs wildcard DNS and certificates before the first push works.
- *A key-collision validator*: the first version of this decision rejected keys equal to
  the first segment of another repository's images. On the repository-path method that
  segment is always the key, so nothing can collide, and the rule only refused valid keys
  ([research 23](../23-docker-oci-registry.md) section 2.2).
- *404 `NAME_UNKNOWN` for remote and virtual Docker repositories*: tells the user the
  repository does not exist when it does. An explicit `UNSUPPORTED` names the reason.
- *`/v2/_catalog` empty or refused, as Artifactory's per-repository catalog suggests*: the
  registry-wide catalog is what `crane catalog <host>` asks for, and prefixing each name
  with its key gives references that pull.

## Artifactory 7.x check

The decision stands against 7.161.32
([research 03](../03-repository-configuration-model.md#artifactory-7x-check)). 7.x adds:
- **Signing key pairs** (`primaryKeyPairRef`, `secondaryKeyPairRef`) that also sign npm
  packages.
- **Default layouts** that `pkg/layout` lacks, Cargo and npm Test ([#354](https://github.com/weaveplatform/artifactweave/issues/354)).
- **npm "Enforce strict layout",** whose REST field is not documented (DV-041).
- **`includesPattern`** defaults to everything when a create request omits it (7.117.5), as
  here.

The delete answers match 7.x ([#397](https://github.com/weaveplatform/artifactweave/issues/397)).
Deleting `artifactory-build-info` answers 403 with "Deletion of Build Info repository
artifactory-build-info is not allowed." A batch that names it is refused before any delete,
with that `statusMessage` and no `reports` ([D]
[deleterepositories](https://docs.jfrog.com/artifactory/reference/deleterepositories.md),
example `buildInfoBlocked`). A blank key, `auto-trashcan` and `jfrog-support-bundles`
answer 400 ([D] [deleterepository](https://docs.jfrog.com/artifactory/reference/deleterepository.md),
status list). A batch of `[]` answers 400 with the `statusMessage` "No repository keys were
provided for deletion" (example `emptyRepoKeyList`), and a batch in which every repository
failed has the `statusMessage` "Repositories batch removal failed" (the 403, 409, 424 and 500
examples). Three points are inferred [I]: the single delete's 403 carries the batch's message
in a failure report, because its page lists 403 for a validation failure but shows no example;
a batch stops at its first refused key, so that key sets the status when it names both kinds;
and a key that differs from `artifactory-build-info` only in case is refused the same way.

The audit in [#378](https://github.com/weaveplatform/artifactweave/issues/378) found where
the Repositories API differs from 7.x. The issue named changes the code, and
[research 03](../03-repository-configuration-model.md#artifactory-7x-check) lists the
differences in the configuration model:
- `GET /api/repositories` gives `packageType` as the type's `RepoType` name, such as `"Generic"`, `"Maven"`, `"Npm"` and `"YUM"` for rpm, and its `packageType` filter matches either spelling without regard to case ([D] [getrepositoriesbytypeandproject](https://docs.jfrog.com/artifactory/reference/getrepositoriesbytypeandproject.md); [S] provider 12.11.14 [`datasource_artifactory_repositories_test.go`](https://github.com/jfrog/terraform-provider-artifactory/blob/v12.11.14/pkg/artifactory/datasource/repository/datasource_artifactory_repositories_test.go) expects `"Alpine"`). `pkg/repo.PackageTypeName` holds the names, and [research 03](../03-repository-configuration-model.md#package-type-names-in-listings) gives the source of each.
- Every read sends an empty `description` and `notes` as `""`, and an administrator's read of a local, remote or federated repository sends empty `propertySets` as `[]` ([D] [getrepositoryconfiguration](https://docs.jfrog.com/artifactory/reference/getrepositoryconfiguration.md), [getbatchofrepositoriesbyname](https://docs.jfrog.com/artifactory/reference/getbatchofrepositoriesbyname.md) and the v2 samples). A read by anyone but an administrator shows `notes` as `""`.
- The v2 read keeps `checksumPolicyType` for the Maven family and `forceMetadataNameVersion` for Helm only, so a generic local repository has neither ([D] [getrepositoryconfigurationv2](https://docs.jfrog.com/artifactory/reference/getrepositoryconfigurationv2.md), sample `genericLocalV2`). That sample also lacks `priorityResolution` and `environments`, which the v2 read still sends for every type: the sample may predate both, and the configurations sample of a generic local repository sends them [I], on [#216](https://github.com/weaveplatform/artifactweave/issues/216).
- 7.x differs: a YAML PATCH can change a repository's package type ([D] [patchsystemconfiguration](https://docs.jfrog.com/administration/reference/patchsystemconfiguration.md): "it is easy to overwrite configurations, such as a repository's package type"). artifactweave refuses the change on every write path, a deviation registered by #378.

## Constraints

- Package types that artifactweave does not implement are accepted and persisted; nothing
  serves their package protocol.
- `terraformbackend` is out of v1 scope. Like the other unimplemented types, it is
  accepted and persisted, with no state API behind it.
- A Docker repository whose key a client cannot send is reachable only through the
  Artifactory REST API; this is documented in the operator guide.
- The subdomain option needs wildcard DNS and a certificate for `*.<base_url host>`; a
  key used as a subdomain must also be a valid DNS label.

## Verification

- `pkg/repo` round-trip tests for every field in the research tables, including string
  and boolean forms per class.
- Validation tests for the package-type sets per class.
- Routing tests for both prefixes, the subdomain option, keys in any case, names that
  contain reserved words and each repository class.
- Terraform acceptance: every in-scope `artifactory_<class>_<type>_repository` resource
  applies, plans clean and destroys.
- OCI conformance against `/v2/<repo>/<image>`.

## References

- [03-repository-configuration-model](../03-repository-configuration-model.md)
- [04-package-protocols](../04-package-protocols.md), Docker section
- [23-docker-oci-registry](../23-docker-oci-registry.md), sections 2.1 to 2.3
- [08-terraform-providers](../08-terraform-providers.md)
- <https://docs.jfrog.com/integrations/docs/configuration-json-files>
