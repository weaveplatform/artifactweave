# 0040: Terraform registry: modules and providers

Status: Proposed

Checked against: **Artifactory 7.161.32** (SaaS 7.174.0) on 2026-10-19 ([research 31 section 16](../31-terraform.md#16-artifactory-7x-check), [#530](https://github.com/weaveplatform/artifactweave/issues/530)).

## Context

Terraform installs modules and providers from a registry it finds through
`https://<host>/.well-known/terraform.json`, and from a network mirror named in its CLI
configuration. Artifactory serves both from local `terraform` repositories whose
`terraformType` is `MODULE` or `PROVIDER`, encodes the repository key into the module or
provider namespace because the discovery document has no repository segment, and serves
providers through the registry protocol since 7.125.4 with a GPG key pair bound to the
repository. Decision [0032](0032-package-format-handlers.md) fixes how a format plugs in,
decision [0003](0003-repository-model.md) what a remote or virtual repository answers
before P9 and P10, and decision [0018](0018-package-index.md) the package index. This
decision fixes what a local or federated Terraform repository answers and stores. The
Terraform backend (`terraformbackend`, and the `tfe.v2*` and `state.v2` services) is
decision 0041 ([#531](https://github.com/weaveplatform/artifactweave/issues/531)).
[Research 31](../31-terraform.md) holds the facts; its section numbers are cited below as
R31.

## Decision

### Packages and routes

The HTTP layer is `internal/server/terraform`, mounted through the protocol router kit of
decision 0032 at `/artifactory/api/terraform/` with the encoded-key resolver and the
host-root mount of [#532](https://github.com/weaveplatform/artifactweave/issues/532). Path
parsing, the index hooks and the hashes are `internal/format/terraform`.

| Method | Path | Answer |
|---|---|---|
| `GET`, `HEAD` | `/.well-known/terraform.json`, at the host root | The discovery document |
| `GET`, `HEAD` | `v1/modules/<repo>__<ns>/<name>/<system>/versions` | A module's versions |
| `GET`, `HEAD` | `v1/modules/<repo>__<ns>/<name>/<system>/<version>/download` | 204 with `X-Terraform-Get` |
| `GET`, `HEAD` | `v1/providers/<repo>-rt-ns-<ns>/<type>/versions` | A provider's versions |
| `GET`, `HEAD` | `v1/providers/<repo>-rt-ns-<ns>/<type>/<version>/download/<os>/<arch>` | A provider package |
| `GET`, `HEAD` | `<repo>/v1/modules/<ns>/<name>/<system>/...`, `<repo>/v1/providers/<ns>/<type>/...` | The same operations, repository-scoped |
| `GET`, `HEAD` | `<repo>/providers/<hostname>/<ns>/<type>/index.json`, `.../<version>.json` | The network mirror |
| `GET`, `HEAD` | `<repo>/v1/modules/<path>?sig=<token>`, `<repo>/providers/<path>?sig=<token>` | A signed file download |
| `POST` | `<repo>/reindex` | The JFrog operation ([#516](https://github.com/weaveplatform/artifactweave/issues/516)) |

Paths are relative to `/artifactory/api/terraform/` unless marked. The backend's paths are
decision 0041's. Any other path answers 404, and any other method on a listed path 405.

A request is dispatched in this order:
1. A request carrying a `sig` query parameter is a signed file download.
2. A path starting `v1/modules/` or `v1/providers/` is the global form, whose repository key
   is encoded in the namespace.
3. Otherwise the first segment is the repository key.

A repository keyed `v1` is therefore reached as `v1/v1/...` or through an encoded namespace,
and its network mirror, at `v1/providers/...`, is not reachable at all. The key is not
refused for that.

### The discovery document

`GET /.well-known/terraform.json` answers 200, `Content-Type: application/json`:

```json
{
  "modules.v1": "<base>/artifactory/api/terraform/v1/modules/",
  "providers.v1": "<base>/artifactory/api/terraform/v1/providers/"
}
```

- `<base>` comes from the base URL resolver (decision
  [0002](0002-spec-first-flat-crud-handwritten-item-paths.md)), so the values are absolute
  and end in a slash.
- **`login.v1` is omitted until
  [#262](https://github.com/weaveplatform/artifactweave/issues/262)** (maintainer decision,
  2026-10-19), owned by P21: the browser approval page is shared with npm web login.
  Until then, decision
  0041's `tfe.v2` service supports Terraform's pasted-token login and account check. Tokens
  also reach Terraform through `credentials.tfrc.json`, a `credentials` block or
  `TF_TOKEN_<host>` (R31 6).
- Decision 0041 adds the backend's services to the same handler.
- The route is served before authentication and ignores any credentials, so an anonymous
  request and one with an expired token both get the document. It is served whether or not
  any Terraform repository exists.

### Repository keys in the path

This is the resolver #532 builds: a function from the raw path to the repository key and the
rest of the path.

- **`<repo>__<ns>`** (modules). The namespace segment is split at each `__` in turn, from
  the left. The first split whose left side names an existing repository wins, and its
  right side is the namespace. Keys are compared without regard to case, as keys are unique
  without regard to case ([research 03](../03-repository-configuration-model.md#key-rules)).
  A segment with no `__`, an empty side, or no left side naming a repository answers 404.
- **`<repo>-rt-ns-<ns>`** (providers) is split the same way at each `-rt-ns-`. Terraform
  sends provider namespaces in lower case (R31 3.4), so the case-blind comparison is what
  makes a key with capitals reachable at all.
- **The repository-scoped forms** take the key from the first segment, as every router kit
  mount does, compared without regard to case as the encoded keys are, and the namespace as
  written.

Once the repository is known, the kit's checks run in this order, the order every router kit
mount has (amended 2026-10-20, [#532](https://github.com/weaveplatform/artifactweave/issues/532)):
1. A repository of another package type answers 404.
2. The class gate: local and federated repositories are served, and remote and virtual
   ones answer 501 naming their class (decisions 0003 and 0032), except the remote module/provider adapters added by #631/#632 below.
3. The route must fit the repository's `terraformType` (below).

### Signed download URLs

Terraform sends no credentials when it downloads a module archive, a provider zip, its
`SHA256SUMS` or its signature (R31 2.1, 3.1). The mirror protocol asks for
"cryptographically-secure, user-specific, and time-limited URLs" (R31 4). So every URL that
artifactweave hands Terraform for a file is signed:

- Module zips are at `<base>/artifactory/api/terraform/<repo>/v1/modules/<path>?sig=<token>`.
  Provider files are at `<base>/artifactory/api/terraform/<repo>/providers/<path>?sig=<token>`,
  the form 7.x's mirror hands out (R31 4). `<path>` is the item path, each segment
  percent-encoded.
- The token is a compact JWS signed with HS256. Its key is derived from the master key
  (decision [0017](0017-sealed-columns-and-master-key.md)), so every node verifies the
  others' URLs. Its claims are:
  - `sub`: the requesting principal, or `anonymous`;
  - `repo` and `path`: the file;
  - `sha256`: the file's content;
  - `iat`, and `exp` 90 seconds later, as 7.x's tokens (R31 2.4).
- A signed request is served only when all of these hold:
  - the signature verifies;
  - `exp` has not passed when the request starts;
  - `repo` and `path` match the URL;
  - the file exists with that SHA-256;
  - the principal still has Read on it.

  Otherwise it answers 403, or 404 when the file is gone. Other credentials on the request
  are ignored.
- The file is served through the router kit's `ServeItem`, so the download redirect of
  decision [0029](0029-cdn-delivery-and-download-redirect.md), `Range` and the download
  counts apply. URLs are made for each answer and never stored.

### Modules

**Storage.** A module version is a zip deployed through the item routes at
`<ns>/<name>/<system>/<version>.zip`, by `PUT` or `jf terraform publish` (R31 2.3, 7). The
item routes store any path, as on any repository. artifactweave writes no `module.json` or
other index file into the repository: the index lives in the package index.

**What is indexed.** A file is a module version when its path has exactly four segments and:
- `<ns>` and `<name>` match Terraform's module name rule: 1 to 64 of `[0-9A-Za-z-_]`,
  starting and ending with a letter or digit;
- `<system>` is 1 to 64 lower-case letters and digits;
- the file name minus `.zip` is a semantic version, `MAJOR.MINOR.PATCH` with optional
  pre-release and build parts, optionally after a `v`.

Any other file is stored and not indexed. The version is the semantic version without its
`v`. When two files name the same version, such as `1.0.0.zip` and `v1.0.0.zip`, the one
whose name sorts first is the version's file, and the other is stored and not indexed.

**`versions`** answers `Content-Type: application/json`:

```json
{"modules":[{"versions":[{"version":"1.0.0"},{"version":"1.1.0"}]}]}
```

- It lists the versions whose zip the caller can read, in ascending semantic-version order,
  each without a `v`.
- A module with no such version answers 404.
- Namespace and name are matched as written, with case, as they are stored and as Terraform
  sends them (R31 2.2).

**`download`** answers 204 with `Content-Length: 0` and `X-Terraform-Get` set to the zip's
signed URL. The requested version, with or without a `v`, names the indexed version equal to
it. Terraform asks for `0.1.2` after listing `v0.1.2` (R31 2.1). An unknown version answers
404.

**Not served.** The public registry's other operations, such as module details, the latest
download, search and listing, currently answer 404. Terraform does not use them
(R31 2.1), but that is not supersession: P20/P28 own contract discovery and the
applicable implementation.

**Overwrites** follow the item routes: different content needs Delete, and the same content
declared by its checksum needs Deploy only
([#400](https://github.com/weaveplatform/artifactweave/issues/400)). There is no
republish refusal, because nothing on the client pins a module's content (R31 2.1).

**`jf terraform publish`** needs only the generic routes (R31 7):
- `GET /api/repositories/<repo>` answering 200 to the deployer;
- the checksum deploy, answering 404 for unknown content so the client sends the zip;
- the deploy answering 201, with the build and VCS properties from the matrix parameters
  stored;
- the build-info that `jf rt build-publish` sends.

### Providers

**What is indexed.** A file is part of a provider version when its path ends in
`<ns>/<type>/<version>/<file>`, where `<file>` is one of:
- `terraform-provider-<type>_<version>_<os>_<arch>.zip`, a package;
- `terraform-provider-<type>_<version>_SHA256SUMS`;
- `terraform-provider-<type>_<version>_SHA256SUMS.sig`;
- `terraform-provider-<type>_<version>_manifest.json`.

The type and version in the file name must equal the folders', the type compared without
regard to case. `<ns>` must be a provider namespace and `<type>` a provider type under
Terraform's rules (R31 3.4). `<version>` is a semantic version without a `v`, and `<os>` and
`<arch>` are each `[a-z0-9]+`. Folders before `<ns>` are ignored, as in JFrog's example,
which adds one (R31 3.3). Any other file, such as HashiCorp's key-specific
`_SHA256SUMS.<keyid>.sig`, is stored and not indexed.

When two paths under different prefixes claim the same package, `SHA256SUMS`, signature or
manifest of one version, the path that sorts first holds it, and the other is stored and not
indexed.

**Visibility.** A provider version is visible to the registry once its `SHA256SUMS`, its
signature and at least one package are indexed. It is visible to the mirror once one package
is indexed.

**`versions`** answers `{"versions":[{"version","protocols","platforms":[{"os","arch"}]}]}`:
- It lists the visible versions with at least one package the caller can read, in ascending
  semantic-version order.
- `protocols` is the manifest's `metadata.protocol_versions` when a valid manifest is
  indexed, and otherwise `["5.0"]`, the default of HashiCorp's own registry (R31 3.1).
- `platforms` lists the readable packages, sorted by `os` and then `arch`.
- A provider with no such version answers 404.

**`download/<os>/<arch>`** answers 404 when the version is not visible or has no package
for that platform. Otherwise it checks:
1. The repository's `primaryKeyPairRef` names a GPG key pair. A `secondaryKeyPairRef` that
   names a GPG pair is used as well.
2. `SHA256SUMS` has a line for the package's file name, and the SHA-256 on that line is the
   stored package's.
3. The signature verifies over `SHA256SUMS` with one of those keys.

A failed check answers **409**, naming what is missing or wrong. Verification runs on each
metadata request using the current key bindings and resolved content snapshots; no cache is
used initially, so rotation cannot reuse a stale success. Otherwise the answer is:

```json
{
  "protocols": ["5.0"],
  "os": "linux",
  "arch": "amd64",
  "filename": "terraform-provider-null_3.3.2_linux_amd64.zip",
  "download_url": "<signed URL of the package>",
  "shasums_url": "<signed URL of SHA256SUMS>",
  "shasums_signature_url": "<signed URL of the signature>",
  "shasum": "<the package's line in SHA256SUMS>",
  "signing_keys": {
    "gpg_public_keys": [
      {"key_id": "<long key ID, upper-case hex>", "ascii_armor": "<public key>",
       "trust_signature": "", "source": "", "source_url": ""}
    ]
  }
}
```

The primary pair's key comes first, then the secondary's.

**Key pairs.** A PROVIDER repository's keys are its `primaryKeyPairRef` and
`secondaryKeyPairRef`, naming pairs made through the keypair API
([research 05](../05-platform-identity-and-security-apis.md)). A create or update of a local
or federated `terraform` repository whose `terraformType` is PROVIDER answers 400 when either
field names an existing pair whose `pairType` is not GPG. A repository without a key pair
still serves the network mirror. Deploys are never refused for a signature or a checksum.

### The network mirror

The mirror serves PROVIDER repositories from the same index, and needs no key pair or
`SHA256SUMS`:

- **`<repo>/providers/<hostname>/<ns>/<type>/index.json`** answers
  `{"versions":{"<version>":{}}}` for the versions with at least one package the caller can
  read, or 404. `<hostname>` must be a valid hostname and is otherwise ignored: a local
  repository serves its providers under any registry hostname, so JFrog's
  `network_mirror` with `source = "kreuzwerker/docker"` works.
- **`.../<version>.json`** answers
  `{"archives":{"<os>_<arch>":{"url":"<signed URL>","hashes":["h1:..."]}}}` for the readable
  packages. `hashes` is left out for a package that is not a valid zip.
- Both answer `Content-Type: application/json`.

The `h1:` hash is computed when the package is indexed, in the `Inspect` stage of decision
0032, with `dirhash.HashZip(path, dirhash.Hash1)`, as Terraform computes it (R31 4).

### `terraformType`

- Values are `MODULE` or `PROVIDER`, read in any case and echoed in upper case. Any other
  value answers 400 on create and update, after the create definition's enum (R31 5). As
  today, the field is valid on local and federated repositories only.
- Once set, it cannot change: an update with another value answers 400. An update may set it
  on a repository that has none, which queues a reindex.
- A repository without it stores files as any repository does, and every Terraform route on
  it answers 404 saying the repository has no Terraform registry type.
- The module routes on a PROVIDER repository, and the provider and mirror routes on a MODULE
  repository, answer 404 in the registry's `errors` envelope. The module route names
  the required MODULE type; an unsupported provider route uses the not-found message.
- A module zip deployed into a PROVIDER repository, or provider files into a MODULE
  repository, answers 201 and is stored and not indexed.

### Package index

This sets the Terraform part of decision 0018:

| | Modules | Providers |
|---|---|---|
| Package | `<ns>/<name>/<system>`, as stored | `<ns>/<type>`, in lower case |
| Version | the version without `v`, made when its zip is stored | the version, made when its first file is stored |
| Files | the zip | the packages, `SHA256SUMS`, the signature and the manifest |
| Per-file metadata | none | for a package: `os`, `arch` and its `h1:` hash |
| Order | `CompareSemver` (`types.ComparatorFor(repo.Terraform)`) | the same |
| Derived work | none; answers are computed on read | none |

### Reindex

`POST /api/terraform/<repo>/reindex` answers 200 `text/plain`. It rebuilds the repository's
Terraform index from the item paths through #516's queued path, recomputing the `h1:`
hashes. Its permission and queueing are #516's, for every type. The "git index" that JFrog's
page also names is a remote's (P9).

### Properties

artifactweave sets no property on module or provider files. A deploy's matrix parameters,
such as `jf`'s build and VCS properties, are stored as on any deploy.

### Errors

Every answer under `/artifactory/api/terraform/` that is not a success is
`{"errors":["<message>"]}`, which `surfaceFor` already picks (R31 12). The discovery route's
405 uses it too. The reindex operation is the exception: it is a JFrog REST operation beside
the registry, and answers its refusals in the Artifactory envelope, as the JFrog operations
beside npm's registry do (decision [0034](0034-npm.md)) **[I]** (amended 2026-10-20,
[#532](https://github.com/weaveplatform/artifactweave/issues/532)).

| Status | When |
|---|---|
| 204 | a module `download` |
| 400 | on the repositories API, in its own envelope: a `terraformType` outside the enum, a change of it, or a non-GPG key pair on a PROVIDER repository |
| 401 | an anonymous caller without Read, with the challenge of decision [0005](0005-authentication-and-tokens.md) |
| 403 | an authenticated caller without Read; a signed URL whose token is invalid, expired or no longer matches the file |
| 404 | an unknown repository or none in the namespace, a repository of another package type, a route that does not fit `terraformType` or a repository without it, an unknown or invisible module, provider, version or platform, no version the caller can read, and a path that is no operation |
| 405 | another method on a listed path |
| 409 | a provider `download` whose repository has no GPG key pair, whose `SHA256SUMS` lacks the package or names another SHA-256, or whose signature does not verify |
| 501 | unimplemented remote provider or virtual operations, naming their class |

### Permissions

| Route | Needs |
|---|---|
| Discovery | nothing: served before authentication |
| Module `versions` | Read on the folder `<ns>/<name>/<system>` (the kit's `RequireFolder`), then lists only versions whose zip the caller can read |
| Module `download` | Read on the zip |
| Provider `versions`, the mirror's `index.json` and `<version>.json` | Read on the repository's root folder, then lists only what the caller can read |
| Provider `download` | Read on the package, `SHA256SUMS` and the signature |
| A signed file | Read on the file by the token's principal, when the request is served |
| Deploys, overwrites, deletes | the item routes' rules |

### Testing against a plain-HTTP host

artifactweave serves plain HTTP, and Terraform requests discovery only over HTTPS (R31 1.1).
Acceptance scenarios ([#556](https://github.com/weaveplatform/artifactweave/issues/556),
[#559](https://github.com/weaveplatform/artifactweave/issues/559)) therefore run this way:

- **The registry and the backend.** The scenario writes a CLI configuration file with a
  `host "127.0.0.1:<port>"` block. Its `services` are copied from
  artifactweave's own discovery document, fetched over plain HTTP, so the URLs Terraform
  uses are the ones artifactweave serves (R31 1.3).
  - The registry uses `.terraformrc` in an isolated `HOME`, so Terraform also loads
    `.terraform.d/credentials.tfrc.json`. Setting `TF_CLI_CONFIG_FILE` disables that
    default credentials-file lookup in Terraform 1.16.4 (R31 7.1).
  - Sources name the host as `127.0.0.1:<port>/<repo>__<ns>/<name>/<system>` and
    `127.0.0.1:<port>/<repo>-rt-ns-<ns>/<type>`, which hold the dot a module host needs.
  - The token goes in `credentials.tfrc.json`, in a `credentials "127.0.0.1:<port>"` block,
    or in `TF_TOKEN_127_0_0_1:<port>` set in the process environment.
  - This needs no TLS and works on any OS. Terraform then fetches no discovery document, so
    the document's shape is covered by server tests and by the step that copies it.
- **The network mirror** must be `https` (R31 1.3). Its scenario puts the `frontWithTLS`
  proxy of the `@go` scenarios in front of the binary. The proxy must send
  `X-JFrog-Override-Base-Url: https://<front>`, so that the signed archive URLs name the
  front, and `SSL_CERT_FILE` names its certificate.
  - Terraform 1.16, built with Go 1.26, reads `SSL_CERT_FILE` on Linux only, so this
    scenario skips on macOS and Windows until Terraform is built with Go 1.27.
  - The mirror's token is in a `credentials` block for `127.0.0.1:<front port>`.
- `terraform_backend.feature` (#559) exercises pasted-token `terraform login`, its
  `account/details` check and reuse of the saved credential. The `login.v1` browser approval
  flow remains #262.

#556 records this arrangement in `docs/testing/ci.md`.

## Rationale

**The discovery document.**
- Absolute values with a trailing slash match the 7.x capture and RTFACT-27433 (R31 1.2), and
  tools that read the document themselves need not resolve relative URLs.
- Terraform resolves both forms, so a reverse proxy only needs to set the base URL headers
  it already sets for npm and Docker.
- Serving the document before authentication follows Terraform's own order: it sends any
  token it has with the discovery request. A refused discovery reads as "failed to request
  discovery document". A refused `versions` request, where the token is first checked,
  reads as "401 Unauthorized".

**Omitting `login.v1`.** Advertising a login flow that answers 404 would send every
`terraform login` to a dead page. With decision 0041's `tfe.v2` service, Terraform instead
uses its pasted-token fallback and validates the token through `account/details` (R31 6).

**Resolving the key.**
- Splitting at the first separator whose left side names a repository handles keys and
  namespaces that contain `__` or `-rt-ns-`, at the cost of a lookup per candidate, which in
  practice is one.
- The case-blind comparison is forced for providers, which Terraform lower-cases. For modules
  it costs nothing, since keys are unique without regard to case.
- The repository-scoped forms are what 7.x's smart remotes and coder's guide use (R31 2.5).
  They also give keys that the encodings cannot carry, such as keys with a `.`, a way in
  through a `host` block.

**Signed URLs.** Terraform sends no credentials for any archive, and HashiCorp prescribes
time-limited, user-specific URLs. 7.x does this with a 90-second HS256 token naming the user
and a SHA-256 (R31 2.4, 4).
- *An unsigned item URL* would work only on repositories that anonymous users can read.
- *A token embedded in the URL* would put a long-lived credential in logs.
- *`.netrc`* would need every user to configure each host twice.

Binding the token to the content's SHA-256 keeps an overwrite between the answer and the
download from serving the new content under the old answer. Checking Read again at download
keeps a revoked permission from outliving the 90 seconds.

**Modules.**
- Listing versions without the `v` gives every tool a semantic version.
- Accepting either spelling on `download` matches Terraform, which strips the `v` that
  `jf terraform publish` puts in its file names (R31 2.1, 2.3).
- No republish refusal: unlike a Go module, a module's content is pinned nowhere on the
  client.
- Keeping the index out of the repository means that a cleanup or a manual delete cannot
  leave a stale `module.json`, as RTFACT-31372 records for 7.x.

**Providers.**
- *Visibility.* It waits for `SHA256SUMS` and the signature because the files arrive by
  separate `PUT`s in any order. Listing a version before then would let `terraform init`
  choose one it cannot verify.
- *Checking on read, not on deploy.* A deploy-time refusal would depend on the order of the
  `PUT`s, and could not follow a key pair replaced later.
- *409 with the reason.* A repository without a key, or a release signed with another key,
  is a server-side problem. The envelope names it for the operator, where Terraform would
  only report a failed signature check.
- *The manifest.* HashiCorp publishes `_manifest.json` with every release, and its default of
  `["5.0"]` is the registry's own (R31 3.1).
- *Serving the secondary key too* lets a publisher rotate keys, since the protocol allows
  several keys.

**The mirror.**
- Ignoring `<hostname>` is what makes JFrog's documented `network_mirror` with
  `kreuzwerker/docker` work against files deployed without a hostname.
- `h1:` is the hash `terraform providers mirror` writes, and Terraform records it in the lock
  file.

**`terraformType`.**
- The JFrog provider's two resources are separate types, so it never changes the value in
  place.
- A change would reinterpret every stored file, so it is refused rather than silently
  reindexed.
- Storing a wrong-type file without indexing it follows the item routes, which store any
  path, as decision 0035 does for Go.

**Alternatives considered:**
- *Relative URLs throughout*: they would be robust behind any proxy. But 7.x is absolute, and
  tools other than Terraform read the discovery document.
- *Item URLs with the request's credentials in `X-Terraform-Get`*: Terraform drops them
  (R31 2.1).
- *Signing `SHA256SUMS` with the repository's private key*: that would make artifactweave the
  signer. JFrog's instructions have the publisher upload the signature.
- *Refusing to create a PROVIDER repository without a key pair*: the JFrog provider's
  resource has no key pair attribute, and the mirror needs no key.
- *A `module.json` per module, as 7.x keeps*: no client reads it.

## Artifactory 7.x check

The decision stands against 7.161.32 and SaaS 7.174.0. The current documentation confirms
what the decision takes from it (R31 16):
- the module and provider source forms and deploy paths;
- the GPG key pair a provider repository needs;
- the per-repository network mirror;
- `terraformType` and its layouts;
- `jf terraform publish` and the reindex operation.

7.125.4 added the provider registry that this decision serves. 7.125.7's `X-Terraform-Get`
fix (RTDEV-64996) concerns virtual repositories (P10). The other Terraform notes from 7.84
concern remote repositories (P9). The signed download URLs follow JFrog's reproductions on
7.x (RTFACT-29753, 29895, 30392). Their form for a local repository, and every other point
marked [I] in research 31, are on
[#216](https://github.com/weaveplatform/artifactweave/issues/216) (R31 14).

## P9 shared retrieval contract

Remote implementation follows the [shared retrieval contract](../../implementation/remote-retrieval-contract.md)
(#613): metadata-only zap, cache aliases with no upstream traffic, bounded synchronous
refresh, typed source errors and transient no-store responses. Retain raw upstream
metadata independently of local indexes, rewrite only the client representation and
preserve signed bytes. Its adapter/field matrix assigns this format's P9 tickets and
P10/later handoffs. These are planned remote rules; the local behavior and current class
gates for remaining adapters retain their implementation tickets. #631 adds the remote module behavior described below.

## P9 remote module implementation (#631)

Status remains **Proposed**. The [operations guide](../../operations/terraform-remote-modules.md)
records exact source forms, limits, authorization, original-byte integrity and native proof.
Remote repositories do not take `terraformType`: the module route selects this adapter,
and provider remotes are implemented by #632. Raw registry metadata is separate from cached archives;
private validated 204 location projections use #665's source contract. All client archives
use current-credential, source-identity and digest-bound signed URLs.

[D] HashiCorp's [module registry protocol](https://developer.hashicorp.com/terraform/internals/module-registry-protocol)
and JFrog's [Terraform repository documentation](https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories#remote-terraform-repository)
provide the registry/storage and source-location contracts. [S] Terraform
[v1.16.4 registry installation](https://github.com/hashicorp/terraform/blob/v1.16.4/internal/initwd/module_install.go)
appends a literal registry subdirectory after fetching the full package. Therefore this
adapter resolves an upstream selector to exactly one real archive directory and rewrites
the client selector to that concrete path; it preserves the complete original archive.
Native cold/offline initialization and apply cover a sibling module reached through a
relative source. Controlled fixtures are implementation evidence, not Artifactory traces.

[I] The constrained GitHub archive mapping and path/credential policies require vendor
observation under #216/P19. Further source-getter, HTTP vanity and Git-specific semantics
stay in scope under [P28 #668](https://github.com/weaveplatform/artifactweave/issues/668).
No generic go-getter capability is presumed to be Artifactory parity solely because the
native client recognizes its syntax.

## Constraints

- **Namespace length.** A module namespace holds the key, `__` and the namespace within 64
  characters (RTFACT-31203). A key with a `.` fits neither encoding, so it needs the
  repository-scoped form through a `host` block.
- **Underscores.** A provider key with `_` needs Terraform 1.16.0 or later (R31 3.4).
- **The host root.** The discovery document is at the host root, so a reverse proxy that
  forwards only `/artifactory/` must also forward `/.well-known/terraform.json`.
- **TLS.** Terraform needs HTTPS for discovery and for the mirror, so a deployment terminates
  TLS in front of artifactweave, as it does for Docker and Go.
- **Short-lived URLs.** A signed URL lives 90 seconds from the answer. A client that waits
  longer runs again.
- **`terraform login`.** The `login.v1` approval flow waits for #262. Decision 0041's
  `tfe.v2` service supports the interim pasted-token flow: Terraform attempts to open
  `https://<host>/app/settings/tokens` and checks the pasted token through `account/details`.
  artifactweave supplies no browser token page; mint an Access token separately. The native
  backend exit checks verify validation, saving and reuse of that token (R31 6).
- **Remote modules/providers** are implemented by #631/#632; virtual resolution remains P10. Additional source-getter qualification is P28/#668.
- **Reindex cost.** A reindex reads every provider package to recompute its `h1:` hash.
- **Unconfirmed points.** What 7.x answers in R31 section 14 is inferred, and on the
  live-capture list (#216) for P19.

## Owned follow-up

P21 owns `login.v1`, PKCE and the shared browser approval flow (#262). P28 owns
module-details/latest-download/search/listing contract discovery and implementation;
absence from Terraform CLI traffic alone does not remove these APIs from scope.
P9/P10 supply remote/virtual registry behavior. P28/P41 must add native OpenTofu
registry, mirror and backend proof; its source audit is not a native executable
acceptance result. State-version outputs and cloud execution remain the separate
work items in decision [0041](0041-terraform-backend.md#owned-follow-up).

The [capability roadmap](../../implementation/capability-roadmap.md) records the
phase boundaries under decision [0042](0042-capability-scope-and-supersession.md).
These assignments describe planned work, not current runtime support.

## Verification

- **`internal/format/terraform` unit tests:**
  - module and provider path parsing: depth, the name rules, `v` versions and duplicates,
    case, `os` and `arch`, and the manifest;
  - the index after deploy, overwrite, delete, copy, move, restore and reindex, through the
    shared contract suite of decision 0032 (#533);
  - `h1:` against the hashes Terraform records for a HashiCorp release;
  - signature verification with a test GPG pair.
- **`internal/server/terraform` tests**, in the per-format status table and route × principal
  matrix of decision 0032:
  - every status above, and the envelope on each refusal;
  - discovery to an anonymous caller and to one with a bad token;
  - the key resolver over keys containing `__` and `-rt-ns-`, capitals, and a key `v1`;
  - signed URLs that are expired, tampered with, outlived by an overwrite, or whose
    principal lost Read;
  - each 409, and 501 for virtual repositories; remote module/provider paths have their P9 fault and integrity tests;
  - the `terraformType` mismatches.
- **Acceptance**, as "Testing against a plain-HTTP host" says (#556):
  - `terraform init` of a module deployed by `PUT` and of one deployed by
    `jf terraform publish`;
  - a provider through the registry with its signature checked, and through the mirror;
  - tokens in `credentials.tfrc.json` and in `TF_TOKEN_<host>`;
  - an anonymous init refused with 401;
  - one root combining a private module and signed provider, selecting `1.2.0` from
    `1.0.0`, `1.2.0` and `2.0.0` under `~> 1.0`, then planning and applying it;
  - module files stored but not served as modules in a PROVIDER repository;
  - REST delete and reindex followed by a fresh module resolution selecting `1.0.0`,
    then trash restore and reindex selecting `1.2.0` again with an unchanged plan.

## References

- [Research 31](../31-terraform.md)
- HashiCorp, `hashicorp/web-unified-docs` `f17b63b7b01c`:
  [remote service discovery](https://developer.hashicorp.com/terraform/internals/remote-service-discovery),
  [module registry protocol](https://developer.hashicorp.com/terraform/internals/module-registry-protocol),
  [provider registry protocol](https://developer.hashicorp.com/terraform/internals/provider-registry-protocol),
  [provider network mirror protocol](https://developer.hashicorp.com/terraform/internals/provider-network-mirror-protocol),
  [login protocol](https://developer.hashicorp.com/terraform/internals/login-protocol)
- Terraform v1.16.4, `internal/registry/client.go`, `internal/getproviders/`,
  `internal/command/login.go`; terraform-svchost v0.2.1; terraform-registry-address v0.5.0
- jfrog-cli-artifactory `4a0f3827cfa0`, `artifactory/commands/terraform/terraformpublish.go`
- <https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories.md>

## P9 remote provider implementation (#632)

Status remains **Proposed**. The [provider operations guide](../../operations/terraform-remote-providers.md)
records origin-registry and network-mirror behavior. Remote provider responses preserve
upstream public-key identity and the exact checksum/signature/archive bytes; local signing
keys are not consulted. A registry package is advertised only after its complete bundle
has passed checksum, detached-signature and archive validation. Signed delivery rechecks
current authority, source/metadata identity and exact content.

[D] The current HashiCorp [registry protocol](https://developer.hashicorp.com/terraform/internals/provider-registry-protocol)
requires package signing keys and signature/checksum locations, while the
[mirror protocol](https://developer.hashicorp.com/terraform/internals/provider-network-mirror-protocol)
provides optional package hashes without GPG fields. The adapter preserves that distinction:
registry-derived mirrors retain server-side upstream signature verification, and raw
Smart Remote mirrors retain their advertised strongest supported hash. Computing a hash
for a hashless mirror does not establish a signer. [D] JFrog names `terraformProvidersUrl`
as provider storage and `terraformRegistryUrl` as registry/API-prefix configuration.

[I] The exact target behavior for upstream mirror-prefix selection, per-host cache paths,
optional-platform discovery bounds and complete-bundle visibility still requires P19/#216
capture. Controlled fixtures and native clients establish this implementation only. JFrog
provider repository CRUD proof remains separate from native package-content qualification.

