# Terraform registry: modules and providers

Research baseline: **2026-10-19**.

Checked against: **Artifactory 7.161.32** (self-managed; SaaS 7.174.0) on 2026-10-19, by [#530](https://github.com/weaveplatform/artifactweave/issues/530); [section 16](#16-artifactory-7x-check) records the check.

Phase P8 serves Terraform modules and providers from local `terraform` repositories.
[Research 04](04-package-protocols.md#terraform-providers-and-modules) sketched the protocol
and [research 17](17-undocumented-shapes.md#terraform-discovery-and-the-remote-backend) a
discovery capture; the 7.x check of research 04 left both to be confirmed claim by claim.
This report collects what Terraform, HashiCorp's protocols, JFrog's documentation, Jira and
the JFrog clients show, and corrects what the earlier research got wrong (section 13). The
Terraform backend (`terraformbackend`, the `tfe.v2*` and `state.v2` services) is research 32
([#531](https://github.com/weaveplatform/artifactweave/issues/531)). Decision
[0040](decisions/0040-terraform.md) records the choices.

Each claim is labelled:

| Label | Means |
|---|---|
| **[D]** | documented at the cited URL, read on 2026-10-19 |
| **[RN]** | stated in the Artifactory release notes or known issues, read on 2026-10-19 |
| **[J]** | recorded in JFrog's public Jira (`https://jfrog.atlassian.net/rest/api/2/issue/<KEY>`), read on 2026-10-19: an issue fixed in a 7.x release, or a JFrog staff comment or reproduction on it ([research README](README.md#target-version-and-sources)) |
| **[C]** | seen in a response from a running Artifactory instance that a user pasted in the cited public issue or discussion; it shows what that instance answered, not documented behaviour |
| **[S]** | seen in source code at the cited commit |
| **[I]** | inferred from the facts above; not stated by any source |

No JFrog knowledge-base article on the Terraform registry was found, so no claim is
labelled [KB].

Sources:
- **HashiCorp's protocol references** in `hashicorp/web-unified-docs` at `f17b63b7b01c`,
  `content/terraform/v1.16.x/docs/internals/`: `remote-service-discovery.mdx`,
  `module-registry-protocol.mdx`, `provider-registry-protocol.mdx`,
  `provider-network-mirror-protocol.mdx`, `login-protocol.mdx`; and
  `language/modules/sources.mdx`, `language/files/dependency-lock.mdx`,
  `cli/config/config-file.mdx`, and `terraform-docs-common/docs/registry/providers/publishing.mdx`
  (published at <https://developer.hashicorp.com/terraform/internals/>).
- **Terraform** `hashicorp/terraform` **v1.16.4**, the version artifactweave's CI pins
  (`.github/workflows/quality-gate.yml`), built with Go 1.26.8 (`.go-version`):
  `commands.go`, `provider_source.go`, `internal/registry/client.go`,
  `internal/initwd/module_install.go`, `internal/getmodules/getter.go`,
  `internal/getproviders/{registry_client,http_mirror_source,hash}.go`,
  `internal/providercache/package_install.go`, `internal/command/login.go` and
  `internal/command/cliconfig/{cliconfig,credentials}.go`. With it,
  `hashicorp/terraform-svchost` **v0.2.1** (`disco/disco.go`, `disco/host.go`) and
  `hashicorp/terraform-registry-address` **v0.5.0** (`module.go`, `provider.go`; Terraform
  1.15.0 pins v0.4.0).
- **JFrog's documentation**, read as `.md` on 2026-10-19:
  [terraform-opentofu-and-terraform-backend-repositories](https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories.md)
  (updatedAt 2026-06-08, "the Terraform page" below),
  [jf-terraform](https://docs.jfrog.com/artifactory/docs/jf-terraform.md) (2026-07-29),
  [calculateterraformindex](https://docs.jfrog.com/artifactory/reference/calculateterraformindex.md) (2026-06-15),
  [security-keys-management](https://docs.jfrog.com/administration/docs/security-keys-management.md) (2026-07-02),
  [local-repositories](https://docs.jfrog.com/artifactory/docs/local-repositories.md) (2026-09-15),
  [configuration-json-files](https://docs.jfrog.com/integrations/docs/configuration-json-files.md) (2026-07-26),
  [createrepository](https://docs.jfrog.com/artifactory/reference/createrepository.md) (2026-05-28) and
  [repository-layouts](https://docs.jfrog.com/artifactory/docs/repository-layouts.md) (2026-09-09).
- **The release notes**: [self-managed](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases)
  (updatedAt 2026-10-05, 7.84.3 to 7.161.32), [SaaS](https://docs.jfrog.com/releases/docs/artifactory-saas-releases)
  (2026-10-19, to 7.174.0) and the [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues) (2026-09-23).
- **Jira**: RTFACT-27433, 29753, 29895, 30074, 30340, 30369, 30392, 30520, 30802, 31026,
  31167, 31203, 31270 and 31372.
- **The JFrog clients**: jfrog-cli-artifactory `4a0f3827cfa0`
  (`artifactory/commands/terraform/terraformpublish.go`), jfrog-client-go `ab72c67e288c`
  (`artifactory/services/upload.go`, `repositories.go`), jfrog-cli-core `b47892ded3a0`
  (`artifactory/utils/utils.go`), and terraform-provider-artifactory **v12.11.14**
  (`pkg/artifactory/resource/repository/local/resource_artifactory_local_terraform_repository.go`,
  `local/local.go`, `default_repo_layout_map.go`, `provider/framework.go`).
- **Captures**: [aquasecurity/trivy discussion 7982](https://github.com/aquasecurity/trivy/discussions/7982)
  (2024-11-21) and coder.com's [Mirror the Coder Registry with JFrog Artifactory](https://coder.com/docs/install/registry-mirror-artifactory).
- **A provider release**: `terraform-provider-null` 3.3.2 at
  <https://releases.hashicorp.com/terraform-provider-null/3.3.2/>.

## 1. Service discovery

### 1.1 The protocol

- **[D]** Terraform maps a hostname to its services by `GET https://<host>/.well-known/terraform.json`.
  Anything but 200 with media type `application/json` and a JSON object means the host offers
  no services; a redirect is followed. Each key is a service identifier
  (`modules.v1`, `providers.v1`, `login.v1`), and each value is a base URL, "either absolute
  or relative", a relative one "resolved against the final discovery URL"
  (remote-service-discovery).
- **[D]** The hostname "must be resolvable in DNS to an address that has an HTTPS server
  running on port 443", or name a port, which must still serve HTTPS.
- **[D]** Credentials configured for the host "will be included in the request for the
  discovery document".
- **[S]** `disco.go` builds the discovery URL with the scheme `https` only (line 266), answers
  a 404 as a host with no services (303), any other non-200 as an error, refuses a media type
  other than `application/json` (316), and reads at most 1 MiB (39). A service URL may be
  `http` or `https` (`host.go:284`): only discovery itself needs TLS.
- **[D]** Base URLs "should generally end with a slash" so the relative operation paths
  resolve beneath them (module-registry-protocol, provider-registry-protocol).

### 1.2 Artifactory's document

- **[C]** A 2024 capture (trivy discussion 7982) shows absolute values and no repository
  segment:
  `"modules.v1": "https://<host>/artifactory/api/terraform/v1/modules/"`;
  `state.v2`, `tfe.v2`, `tfe.v2.1` and `tfe.v2.2` all `https://<host>/artifactory/api/terraform/remote/v2`
  (research 32); `login.v1` with `client: "terraform-cli"`,
  `authz: "https://<host>/ui/terraform/oauth2/authorize"`,
  `token: "https://<host>/artifactory/api/oauth2/token"`, `grant_types: ["authz_code"]` and
  **no `ports`**. The pretty-printed body (`"key" : value`) suggests Jackson's printer.
- **[J]** [RTFACT-27433](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-27433) (Done,
  affects 7.42.5): the `modules.v1` value lacked its trailing slash and was fixed to end in one.
- **[J]** [RTFACT-31167](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-31167) (a
  2025-05 request): "the `providers.v1` URI endpoint in the `.well-known/terraform.json` is
  missing from the response served by Artifactory", since local repositories then offered
  only the network mirror.
- **[RN]** 7.125.4 (SaaS 7.125.3) makes Artifactory "a fully compliant Provider Origin
  Registry", which needs `providers.v1` in the document. Its value is not captured; by analogy
  with `modules.v1` it is `https://<host>/artifactory/api/terraform/v1/providers/` **[I]**.
- **[D]** The Terraform page has `terraform login [JFrogPlatformURL]` followed by "Click
  Approve on the JFrog Platform login page", which is the `login.v1` flow.
- **[I]** 7.x serves the document without authentication: `terraform login` reads `login.v1`
  from it before the client has a token.

### 1.3 A plain-HTTP test host

- **[S]** The CLI configuration's `host "<hostname>" { services = { ... } }` block
  (`cliconfig.go:60`) calls `ForceHostServices` for that hostname (`commands.go:78`), which
  replaces discovery: Terraform then requests no `/.well-known/terraform.json` for it and
  uses the given URLs, which may be `http` (1.1). HashiCorp's CLI configuration page does not
  document the block.
- **[D]** coder.com's Artifactory guide uses that block to point `modules.v1` at a repository:
  `host "<artifactory-host>" { services = { "modules.v1" = "https://<artifactory-host>/artifactory/api/terraform/coder-registry/v1/modules/" } }`,
  because "Artifactory's global service discovery endpoint doesn't include the repository
  name in the modules path".
- **[S]** Credentials are looked up by the user-facing hostname, not by the service URL's
  host: the module registry client adds the host's credentials to each request
  (`internal/registry/client.go:176`), and so does the provider registry client.
- **[S]** A `TF_TOKEN_<host>` variable names the host with `_` for `.` and `__` for `-`
  (`credentials.go:141-147`); the rest goes through `svchost.ForComparison`, so a port
  survives when the variable is set by a process rather than a shell, as
  `TF_TOKEN_127_0_0_1:8081`.
- **[S]** The network mirror alone must be `https`: Terraform refuses another scheme
  (`provider_source.go:213`, "the mirror must be at an https: URL"), and
  `http_mirror_source.go:81` panics on one.
- **[S]** Terraform 1.16.4 is built with Go 1.26.8, whose `crypto/x509` reads `SSL_CERT_FILE`
  on Unix systems other than macOS ([Go's `crypto/x509`](https://pkg.go.dev/crypto/x509#SystemCertPool)),
  so not on macOS or Windows. The `@go` acceptance scenarios already front the binary with a
  TLS proxy whose certificate is passed in `SSL_CERT_FILE`
  (`test/acceptance/client_steps_test.go`, `frontWithTLS`; [ci.md](../testing/ci.md)). That
  proxy, Go's `httputil.NewSingleHostReverseProxy`, keeps the front's `Host` but sends no
  `X-Forwarded-Proto`, so absolute URLs the binary builds name the front with `http`.

## 2. Modules

### 2.1 The protocol and Terraform's requests

- **[D]** A module address is `hostname/namespace/name/system`. `GET <modules.v1>:namespace/:name/:system/versions`
  answers `{"modules":[{"versions":[{"version":"1.0.0"}]}]}`, a single-element list, or 404.
  `GET .../:version/download` answers 204 with `X-Terraform-Get`, which holds a module source
  address, or a relative URL starting with `/`, `./` or `../` that is resolved against the
  download URL (module-registry-protocol).
- **[S]** `client.go`: `versions` answers 200 or 404 and any other status is an error that
  shows only the status (lines 152-157); `download` accepts 200 or 204 (237), shows the body
  of any other answer in its error, and treats only those three prefixes as relative (263).
- **[S]** `module_install.go:477` parses each listed version with `hashicorp/go-version`,
  which accepts a leading `v`, and requests the download with `latestMatch.String()` (591),
  which prints the version without the `v`. A list entry `v0.1.2` is therefore downloaded as
  `.../0.1.2/download`.
- **[S]** The archive is fetched by go-getter (`module_install.go:631`) with
  `Netrc: true` and nothing else (`getmodules/getter.go:85`): Terraform does not send the
  registry token to the `X-Terraform-Get` location.
- **[D]** "If an HTTP/HTTPS URL requires authentication credentials, use a `.netrc` file";
  a URL whose path ends in `.zip` is unpacked as an archive (language/modules/sources,
  "HTTP URLs" and "Fetching archives over HTTP").
- **[D]** "The dependency lock file tracks only provider dependencies. Terraform does not
  remember version selections for remote modules" (dependency-lock): nothing on the client
  pins a module zip's content.

### 2.2 Addresses and the `__` encoding

- **[D]** A module source on Artifactory is
  `[JFrogPlatformURL]/<REPOSITORY-KEY>__<NAMESPACE>/<MODULE-NAME>/<PROVIDER>`, for example
  `company.jfrog.io/terraform-local__terraform-aws-modules/vpc/aws` (the Terraform page,
  "Resolve Terraform Modules").
- **[J]** The requests reach `/artifactory/api/terraform/v1/modules/<repo>__<ns>/<name>/<system>/versions`
  and `.../<ver>/download` (JFrog reproductions on 7.x:
  [RTFACT-30074](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30074) on 7.59.11 and
  7.68.14, [RTFACT-29895](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29895),
  [RTFACT-30392](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30392)). A single
  underscore, `terraform-remote-src_hashicorp`, answers 404
  ([RTFACT-30369](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30369)).
- **[S]** terraform-registry-address v0.5.0 `module.go:39` admits a namespace or name of 1 to
  64 characters from `[0-9A-Za-z-_]`, starting and ending with a letter or digit, and a
  system of 1 to 64 lower-case letters and digits (line 40). It keeps the case of the
  namespace and name, "because existing module registries might be doing case-sensitive
  matching". The hostname must contain a dot.
- **[S]** So `<repo>__<ns>` must fit 64 characters, and a repository key with a `.`, or one
  that starts with `-` or `_`, cannot appear in it. Repository keys may contain `.`
  ([research 03](03-repository-configuration-model.md#key-rules)).
- **[J]** [RTFACT-31203](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-31203) (open, a
  customer's request): Terraform refuses `<repo>__<ns>` over 64 characters with "must be
  between one and 64 characters", and the customer shortens repository keys.
- **[I]** How 7.x splits a namespace whose key or namespace itself contains `__`, and whether
  it matches the key without regard to case, is not shown.

### 2.3 Storage and deploy

- **[D]** A module is deployed with
  `PUT /artifactory/<REPOSITORY-KEY>/<NAMESPACE>/<MODULE-NAME>/<PROVIDER-NAME>/<VERSION>.zip`
  (the Terraform page, "Deploy Terraform Modules via API"), "by checksum or from an archive".
  `terraform-module-default` is `[namespace]/[module-name]/[provider]/[version].[ext]`
  ([research 03](03-repository-configuration-model.md#exported-7x-descriptors)).
- **[D]** `jf terraform publish` deploys `namespace/moduleName/provider/tag.zip`, the module
  name being the module directory's name, and the example tags are `v0.1.2` and `v0.0.1`
  (the Terraform page; jf-terraform), so stored versions carry a `v`.
- **[J]** [RTFACT-31372](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-31372) (closed,
  a customer's report): a cleanup plugin deleted a module's `module.json`, and the module
  stopped resolving until the index was recalculated. 7.x keeps index files in the
  repository; where and what they hold is not shown **[I]**. Remote caches keep the
  registry's answers under `.terraform/`
  ([RTFACT-30802](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30802), JFrog's
  comment; [RTFACT-31026](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-31026)).
- **[J]** [RTFACT-30074](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30074) (closed,
  JFrog's reproduction on 7.68.14): content copied from a remote cache into a local
  repository did not resolve from the local repository, for modules and providers alike.

### 2.4 Download URLs

- **[J]** 7.x hands out absolute, signed download URLs. JFrog's reproductions show
  `X-Terraform-Get: https://<host>/artifactory/api/terraform/<repo>/v1/modules/download/...?sig=<JWT>`
  for remote modules ([RTFACT-29895](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29895),
  [RTFACT-30392](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30392),
  [RTFACT-30340](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30340)), and the request
  log names the caller `signed:_system_` or `signed:anonymous`. The decoded `sig` is an
  HS256 token with `iss` (the requesting user, `anonymous` or `_system_`), `sum` (a
  SHA-256), `internal_repo` (the repository), `ui.request`, `iat` and `exp`, and `exp` is
  `iat` plus 90 seconds in all three captures.
- **[RN]** 7.125.7 (RTDEV-64996) fixed module downloads through a virtual repository "when
  the module's namespace matched the local repository name. The `X-Terraform-Get` header now
  correctly includes the complete module path."
- **[I]** What `X-Terraform-Get` names for a module in a local repository, and whether it is
  signed there too, is not captured.

### 2.5 The repository-scoped form

- **[J]** A smart remote's "Registry URL" is `<base_Artifactory_URL>/artifactory/api/terraform/<repo_key>`
  ([D] the Terraform page, "Remote Terraform Repository"), and 7.x then asks the upstream
  `https://<host>/artifactory/api/terraform/<repo>/v1/modules/<ns>/<name>/<system>/<ver>/download`
  ([RTFACT-30392](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30392), its debug log).
- **[D]** coder.com's guide queries
  `/artifactory/api/terraform/coder-registry/v1/modules/coder/code-server/coder/versions`
  and names `.../api/terraform/<repo>/v1/modules/` as `modules.v1` in a `host` block (1.3).
  The namespace is then written without the repository key.
- **[I]** The same holds for providers at `/artifactory/api/terraform/<repo>/v1/providers/`:
  a remote lists provider versions through its Registry URL
  (`https://registry.terraform.io/v1/providers/<ns>/<type>/versions` in RTFACT-31026's log),
  so a smart remote's upstream must answer that path.

## 3. Providers

### 3.1 The protocol

- **[D]** `GET <providers.v1>:namespace/:type/versions` answers
  `{"versions":[{"version","protocols","platforms":[{"os","arch"}]}]}`, or 404.
  `GET .../:version/download/:os/:arch` answers `protocols` (required), `os` and `arch`
  (echoed), `filename`, `download_url`, `shasums_url`, `shasums_signature_url`, `shasum`, and
  `signing_keys.gpg_public_keys[]` with `key_id` ("uppercase-hexadecimal") and `ascii_armor`
  required, `trust_signature`, `source` and `source_url` in the sample; 404 when the platform
  is not available. Relative URLs resolve against the answering URL. The `.sig` is a binary
  detached GPG signature of the `SHA256SUMS` document "signed by one of the keys indicated in
  the `signing_keys` property" (provider-registry-protocol).
- **[S]** `registry_client.go`: 401 and 403 are reported as missing or rejected credentials,
  404 as an unknown provider or platform, any other status as a failed query that shows only
  the status. `download_url` must be `http` or `https` (288). `shasums_url` and
  `shasums_signature_url` are fetched with `c.httpClient.Get` (433), without credentials, and
  the zip by go-getter with `Netrc: true` (`package_install.go:36`): Terraform sends no
  registry token for any of the three.
- **[D]** HashiCorp's release of a provider holds `terraform-provider-<type>_<ver>_<os>_<arch>.zip`
  per platform, `_SHA256SUMS`, `_SHA256SUMS.sig` and `_manifest.json`, whose
  `metadata.protocol_versions` lists the provider's protocols and "Defaults to `["5.0"]`"
  (publishing.mdx). `terraform-provider-null` 3.3.2 publishes exactly these, its manifest
  giving `["5.0"]`, plus a key-specific `_SHA256SUMS.72D7468F.sig`.

### 3.2 Artifactory's provider registry

- **[RN]** 7.125.4 (SaaS 7.125.3): "Artifactory now natively supports the HashiCorp Terraform
  Provider Registry Protocol ... This new method applies to local, virtual, and federated
  repositories and adds to the `network_mirror` approach."
- **[D]** A provider source is `[JFrogPlatformURL]/<REPOSITORY-KEY>-rt-ns-<NAMESPACE>/<PROVIDER>`,
  "Include `rt-ns` after the repository name to differentiate the repository name from the
  namespace", for example `company.jfrog.io/terraform-local-rt-ns-kreuzwerker/docker`. It
  needs no `.terraformrc` change (the Terraform page).
- **[D]** "To use Terraform Providers as Origin Registry, you must associate a GPG public key
  while creating a local repository", after uploading a GPG pair in Keys Management (the
  Terraform page). The keys page lists GPG for the "Terraform provider registry", for
  "GPG-based signing to verify providers served from your Terraform/OpenTofu repository".
- **[D]** A local repository's JSON carries `primaryKeyPairRef` and `secondaryKeyPairRef`
  (configuration-json-files; createrepository). That the Terraform repository's GPG key is
  `primaryKeyPairRef` is **[I]**; key pairs are created through the keypair API of
  [research 05](05-platform-identity-and-security-apis.md).
- **[I]** Whether 7.x signs anything itself, verifies the uploaded `.sig` or the zips against
  `SHA256SUMS`, and what it answers for a repository without a key pair, is not documented.
  The deploy instructions upload the `.sig`, so the publisher signs with the private key of
  the pair bound to the repository.

### 3.3 Files and layout

- **[D]** The three files are deployed to
  `<REPOSITORY-KEY>/<NAMESPACE>/<PROVIDER-NAME>/<VERSION>/terraform-provider-<PROVIDER-NAME>_<VERSION>_<OS>_<ARCH>.zip`,
  `..._<VERSION>_SHA256SUMS` and `..._SHA256SUMS.sig`; the example adds a folder before the
  namespace, `Terraform-local/terraform-main-provider/hashicorp/null/3.1.0/...` (the Terraform
  page, "Deploy Terraform Providers via API"). The manifest is not mentioned.
- **[D]** `terraform-provider-default` is
  `[namespace]/[provider-name]/[version]/terraform-provider-[provider-name]_[version]_[os]_[arch].[ext]`
  ([research 03](03-repository-configuration-model.md#exported-7x-descriptors)); like the
  module layout it uses tokens Artifactory's parser drops, so it matches no path there.

### 3.4 Addresses and the `-rt-ns-` encoding

- **[S]** terraform-registry-address v0.5.0 `provider.go:525-600`: a provider namespace may
  hold letters, digits, `-` and `_`, not `.`, not `--`, and no leading or trailing `-` or `_`;
  a type holds letters, digits and `-`. Both are case-folded to lower case. v0.4.0, which
  Terraform 1.15.0 pins, allows no `_` in a namespace.
- **[S]** So `<repo>-rt-ns-<ns>` reaches the server in lower case whatever the key's case, a
  key with `.` or `--` cannot be used, and a key with `_` needs Terraform 1.16.0 or later.

### 3.5 Provider implementation check (2026-10-20)

Re-read HashiCorp's [provider registry](https://developer.hashicorp.com/terraform/internals/provider-registry-protocol)
and [network mirror](https://developer.hashicorp.com/terraform/internals/provider-network-mirror-protocol)
references, JFrog's [Terraform repository documentation](https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories.md),
and [security keys documentation](https://docs.jfrog.com/administration/docs/security-keys-management.md).
The local provider contract remains as recorded above; release-note changes to upstream
URL validation and remote caches concern P9.

`TestTerraformProviderInstall` and `@terraform_providers` use the official
[null provider 3.3.2 archives](https://releases.hashicorp.com/terraform-provider-null/3.3.2/)
with a throwaway test GPG key and Terraform 1.16.4. Registry installation verifies the
key identity and produces an `h1:` lock entry; altered checksum/signature files return
409, and anonymous initialization returns 401. The Linux acceptance mirror has no
repository key or sidecars, uses an arbitrary valid upstream hostname, and installs
through HTTPS. It is intentionally skipped on local macOS/Windows for the pinned
client's certificate behavior described in section 1.3. These are artifactweave client
checks, not captures from an Artifactory instance.

## 4. The network mirror

- **[D]** A mirror is configured by `network_mirror { url = "https://..." }`, never through
  discovery. `GET :hostname/:namespace/:type/index.json` answers `{"versions":{"2.0.0":{}}}`
  and `GET :hostname/:namespace/:type/:version.json` answers
  `{"archives":{"linux_amd64":{"url","hashes":["h1:..."]}}}`, `url` relative to the JSON's
  URL. "Terraform does _not_ send credentials when retrieving the archives"; a mirror that
  considers them sensitive "must use cryptographically-secure, user-specific, and
  time-limited URLs". Credentials for a non-default port name the port
  (provider-network-mirror-protocol).
- **[S]** `http_mirror_source.go` requires `application/json` on both answers and accepts
  `h1:` and `zh:` hashes; `hash.go:304` computes `h1:` from a zip with
  `golang.org/x/mod/sumdb/dirhash.HashZip(archivePath, dirhash.Hash1)`.
- **[D]** Artifactory's mirror is `https://[JFrogPlatformURL]/artifactory/api/terraform/<REPOSITORY-KEY>/providers/`,
  with `direct { exclude = ["registry.terraform.io/*/*"] }`, for providers deployed at
  `<NAMESPACE>/<PROVIDER-NAME>/<VERSION>/terraform-provider-..._<OS>_<ARCH>.zip`, resolved
  from sources such as `kreuzwerker/docker` (the Terraform page).
- **[J]** JFrog's reproduction on 7.x
  ([RTFACT-29753](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29753)) shows the
  requests `GET /artifactory/api/terraform/<repo>/providers/registry.terraform.io/hashicorp/random/index.json`
  and `.../3.5.1.json`, and archive URLs of the form
  `https://<host>/artifactory/api/terraform/<repo>/providers/terraform-provider-random/3.5.1/terraform-provider-random_3.5.1_darwin_amd64.zip?sig=<JWT>`,
  signed for 90 seconds as in 2.4.
- **[I]** For a local repository, whether 7.x reads `:hostname` at all, and which hashes it
  sends, is not shown.

## 5. `terraformType`

- **[D]** `"terraformType": "MODULE"|"PROVIDER"`, optional, on local repositories
  (configuration-json-files); the create definition's enum is `MODULE`, `PROVIDER`
  (createrepository). [RTFACT-30520](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30520)
  (Done) added it, and `terraformbackend`, to that page.
- **[D]** "Terraform Registry Type ... Module: Applies the `terraform-module-default`
  repository layout ... Provider: Applies the `terraform-provider-default` repository layout"
  (local-repositories). "The remote and the virtual repository settings are identical for
  both Providers and Modules" (the Terraform page).
- **[S]** The JFrog provider's `artifactory_local_terraform_module_repository` and
  `artifactory_local_terraform_provider_repository` send `packageType: "terraform"` with
  `terraformType` `"module"` or `"provider"`, lower case, and do not read it back
  (`resource_artifactory_local_terraform_repository.go:95-131`). Its map offers
  `terraform_module` and `terraform_provider` on every class and bare `terraform` on remote,
  virtual and federated only, with `simple-default` (`default_repo_layout_map.go`).
- **[I]** What 7.x answers to module routes on a PROVIDER repository and the reverse, to a
  module zip deployed into a PROVIDER repository, to a repository without `terraformType`,
  and to a change of `terraformType` on update, is not documented. RTFACT-30074 found the
  same 404 "regardless of the Terraform Registry Type".

## 6. Authentication and `terraform login`

- **[D]** `terraform login` runs an OAuth 2.0 authorization code grant with PKCE against
  `login.v1`'s `authz` and `token`; Terraform supports no refresh token or expiry, and asks
  for a new login once a token is refused (login-protocol).
- **[S]** Without `login.v1`, `terraform login` looks for `tfe.v2` (`login.go:142`): with
  neither it fails with "Host does not support Terraform tokens API"; with `tfe.v2` it opens
  `https://<host>/app/settings/tokens?source=terraform-login` (601) and asks the user to
  paste a token, which it checks with go-tfe's `Users.ReadCurrent` against `tfe.v2` before
  saving it.
- **[D]** Tokens are stored in `credentials.tfrc.json`, in `credentials "<host>" { token }`
  blocks, or in `TF_TOKEN_<host>`, which wins over the file (config-file, "Credentials"), and
  are sent as `Authorization: Bearer` to the host's services.
- **[D]** The Terraform page has the user generate an identity token and write it to
  `~/.terraform.d/credentials.tfrc.json`; its sample keys the token under `""` instead of the
  host, which Terraform would not match **[S]** (`credentials.go`). Its "Environment Variable
  Configuration" names `JFROG_URL` and `JFROG_ACCESS_TOKEN`; those configure the JFrog
  Terraform provider (`provider/framework.go:151-152`), not the registry, for which Terraform
  reads only `TF_TOKEN_<host>` **[S]**.
- **[RN]** 7.98.7 (RTDEV-46304, "Critical" in 7.90.7) fixed Terraform anonymous requests
  causing 401 for other anonymous requests made at the same time.

## 7. The JFrog CLI

- **[D]** `jf terraform-config` (`tfc`) writes `.jfrog/projects/terraform.yaml` with a
  `deployer` section only; `jf terraform publish` (`jf tf p`) needs `--namespace`,
  `--provider` and `--tag`, takes `--exclusions`, `--build-name`, `--build-number`,
  `--module` and `--project`, and "never invokes the `terraform` binary" (jf-terraform).
  It publishes modules only; providers are deployed with `PUT`.
- **[S]** `terraformpublish.go`:
  - it refuses a publish without the three flags (line 112) and checks the repository with
    jfrog-client-go's `IsExists`, a `GET /api/repositories/<repo>` that must answer 200
    (`repositories.go:38-45`, called through jfrog-cli-core `utils.go:378`);
  - it walks the working directory and treats each directory holding a regular `.tf` file
    as a module, without descending into it (242-272, 340-362);
  - the target is `path.Join(repo, namespace, moduleName, provider, tag+".zip")` (336), the
    tag used as given;
  - the upload zips the module directory's contents, entries relative to it, excluding
    `*.git`, `*.DS_Store` and the user's exclusions (317-332), with the build properties and
    CI VCS properties as matrix parameters;
  - the build-info records the zips with module type `terraform` (303).
- **[S]** jfrog-client-go `upload.go`: an archive is zipped twice, once to size and checksum
  it and once to stream it (lines 851-867); a zip of 10 KiB or more is first offered as a
  checksum deploy (`X-Checksum-Deploy: true` with its checksums, 505-506 and 684-693), and on
  any other answer sent in full with `X-Checksum-Sha1`, `X-Checksum-Md5` and `X-Checksum`;
  200, 201 and 202 count as success (1202-1204).

### 7.1 Pinned-client publishing check (2026-10-20)

The `@terraform_publish` acceptance scenario and `TestJFTerraformPublish` run
**JFrog CLI 2.126.0** and **Terraform 1.16.4** against artifactweave. This is
client-compatibility evidence, not a capture from Artifactory. The commands are:

```sh
jf terraform-config --server-id-deploy=aw --repo-deploy=tf-publish
jf terraform publish --namespace=acme --provider=aws --tag=v1.2.3 \
  --build-name=terraform-publish --build-number=1
jf rt bp terraform-publish 1
```

Two source directories, `alpha/` and `beta/`, each contain a `.tf` file. The actual
publish command checks the repository descriptor, then PUTs
`acme/{alpha,beta}/aws/v1.2.3.zip` through ordinary item deployment with
`build.name`, `build.number` and `build.timestamp` matrix properties. No new
Terraform-specific publishing endpoint is needed. `jf rt bp` sends build-info to
`PUT /api/build`. Its default output is **one build-info module**, ID
`terraform-publish` and type `terraform`, containing **two artifact entries**;
these are distinct from the two Terraform source modules. The test compares each
artifact's SHA1, MD5 and SHA256 with its exact stored archive path and checks the
build module identity.

Terraform then initializes the published module with an Access token carrying
`applied-permissions/user` and a path-scoped Read grant. The token works in
`~/.terraform.d/credentials.tfrc.json` and in `TF_TOKEN_<host>`; the environment
value overrides an invalid file token, while anonymous init returns 401.
Discovery remains public even with an invalid credential. The native test uses
`.terraformrc` for its HTTP service override: setting `TF_CLI_CONFIG_FILE` instead
would deliberately disable loading the default credentials directory in
[Terraform 1.16.4's CLI configuration loader](https://github.com/hashicorp/terraform/blob/v1.16.4/internal/command/cliconfig/cliconfig.go).
The environment precedence matches the
[official CLI configuration reference](https://developer.hashicorp.com/terraform/cli/config/config-file).

Re-read JFrog's [Terraform CLI documentation](https://docs.jfrog.com/artifactory/docs/jf-terraform.md)
(updated 2026-07-29) and the
[self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases.md)
through 7.161.32 on 2026-10-20. The documented publish contract still matches the
pinned client. Recent Terraform URL-validation and remote-repository fixes do not
change this local module publishing flow. The remote backend has its own decision
and native exit checks (0041, #559).

### 7.2 Combined registry exit proof (#556)

The `@terraform_registry` acceptance scenario runs Terraform 1.16.4 against the
binary with a real private module and the pinned null provider 3.3.2, re-signed by
the same fixture key used in `@terraform_providers`. A `~> 1.0` module constraint
selects `1.2.0` from three REST-deployed releases (`1.0.0`, `1.2.0`, `2.0.0`); both
Terraform's module manifest and installed HCL confirm that selection. The module's
output becomes a `null_resource` input in a successful plan and apply.

The scenario first proves anonymous init receives 401, including the server's
`errors` envelope. It then supplies `TF_TOKEN_<host>` with an invalid saved token
present, proving environment precedence in the acceptance job. REST deletion of
`1.2.0` followed by reindex makes a fresh module resolution select `1.0.0`; trash
restore and reindex restore selection of `1.2.0` and a plan with no changes. Only
the module cache is removed, so provider extraction is reused. Reindex preserves
the item tree's current contents; rebuilding an empty index is covered by the
format contract suite.

A module zip deployed to a PROVIDER repository remains downloadable as an item
(201 deploy, 200 read), while its module registry route returns 404. The inverse
provider-route mismatch also returns 404. These checks confirm artifactweave's
decision 0040 behavior; the exact Artifactory mismatch messages remain on #216's
live-capture list. Existing `@terraform_publish` and `@terraform_providers` scenarios
retain native JFrog publication, credentials-file authentication, GPG-verified
registry installation and the HTTPS mirror. Registry host overrides copy the
binary's actual discovery document rather than constructing its service URLs.

## 8. Reindex

- **[D]** `POST /api/terraform/{repoKey}/reindex` "Calculates Terraform Modules or Providers
  index", answering 200 `text/plain` (calculateterraformindex). The Terraform page: an
  "asynchronous re-indexing of a local Terraform repository ... This will also reindex the
  git index and, as a result, will also index the remote repositories"; the UI's Recalculate
  Index "Requires Admin privileges".
- **[J]** RTFACT-31372: a recalculation restored a module whose index file had been deleted.

## 9. Properties

- **[I]** No JFrog source names a property set on module or provider files. The Terraform
  page and jf-terraform mention none, the JFrog properties page lists none for Terraform,
  and `jf tf p` sends only build and VCS properties (section 7).

## 10. The JFrog Terraform provider

- **[S]** v12.11.14 has `artifactory_local_terraform_module_repository` and
  `artifactory_local_terraform_provider_repository` (section 5), with the common local
  attributes and `repo_layout_ref` defaulting to `terraform-module-default` or
  `terraform-provider-default`; neither has a key pair attribute
  (`local_terraform_provider_repository.md`). The local `RepositoryBaseParams` decodes
  `priorityResolution`, `downloadRedirect`, `cdnRedirect`, `blackedOut` and
  `archiveBrowsingEnabled` as Go booleans (`local/local.go:228-246`).
- **[S]** The federated Terraform resource sends `terraformType` too
  ([research 03](03-repository-configuration-model.md#federated-fields)); the remote one
  defaults `terraform_registry_url` to `https://registry.terraform.io`.

## 11. Remote repositories, recorded for P9

- **[RN]** 7.161.15 (also 7.146.34; SaaS 7.161.13): "Breaking Change for Remote Terraform
  Repositories ... hardened controls on external Terraform URLs", answering `External URL is
  not allowed` until an administrator enables external dependency rewrite for the URL. The
  fix for CVE-2026-65924 ("validate Terraform external provider URLs to prevent server-side
  request forgery") is also in 7.133.27, 7.125.18, 7.117.25 and 7.111.18.
- **[RN]** SaaS 7.118.2: remote repositories proxy GitLab and Terraform Cloud registries with
  a bearer token and token authentication.
- **[D]** From 7.39.4 Artifactory rewrites the source of a module it proxies, unless
  `artifactory.terraform.override.modules.source.hostname=false` (the Terraform page);
  [RTFACT-31270](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-31270) (Done) extended
  the rewrite to `//modules/...` submodules.
- **[RN]** Known issues 7.49 and 7.50: the public registry's CDN answers 405 to `HEAD`, so a
  remote needs "Bypass HEAD Requests".

## 12. artifactweave at the baseline

- **[S]** `pkg/repo/repo.go:149-155` upper-cases any `terraformType` and accepts any value;
  `validate.go:90` refuses it on remote and virtual repositories. `DefaultLayout` maps MODULE
  and PROVIDER to the two layouts (`packagetypes.go:99-133`).
- **[S]** `pkg/layout/bundled.go` bundles both Terraform layouts as 7.x defines them; they
  build and match no path (research 03).
- **[S]** `internal/server/stubs.go:23-24` already answers errors under
  `/artifactory/api/terraform/` with `{"errors":["..."]}` (`apierror.TerraformRegistry`).
- **[S]** `POST /api/terraform/{repoKey}/reindex` is served (`internal/server/reindex.go:221`).
- **[S]** No Terraform format exists: `format.For` answers the generic handler, which keeps
  no index, and nothing is served at `/.well-known/terraform.json`.
- **[S]** `types.ComparatorFor(repo.Terraform)` is `CompareSemver` (`pkg/types/version.go:48`).

## 13. Corrections to earlier research

- Research 04 gave `login.v1` a `ports` member; the capture has none (1.2).
- Research 04 gave provider versions `protocols:["5.0"]` as fixed; protocols come from the
  release's manifest, defaulting to `["5.0"]` (3.1).
- Research 04 left open what `X-Terraform-Get` and the provider download URLs name.
  Terraform sends no credentials to them, and 7.x hands out signed URLs (2.4, 4).
- Research 04 dated the provider registry "7.125+"; the release is 7.125.4 (SaaS 7.125.3).
- Research 17 had artifactweave emit `login.v1` and a relative `providers.v1`; decision 0040
  omits `login.v1` until [#262](https://github.com/weaveplatform/artifactweave/issues/262)
  and makes both values absolute.
- Research 08 said a `terraformbackend` repository "is rejected with 400", where research 03
  and 12 say it is accepted and persisted, as `pkg/repo` does; and it said
  `priorityResolution`, `downloadRedirect`, `cdnRedirect`, `enableFileListsIndexing` and
  `blockPushingSchema1` are strings on local repositories, where only JFrog's JSON page types
  them so and the provider decodes booleans (section 10;
  [research 03](03-repository-configuration-model.md#typing-traps)).

## 14. Inferred and undocumented

These go to the live-capture list ([#216](https://github.com/weaveplatform/artifactweave/issues/216)):

1. The value of `providers.v1` on a 7.125 or later instance, and whether the document is
   served without authentication and with a bad token (1.2).
2. How 7.x splits `<repo>__<ns>` and `<repo>-rt-ns-<ns>` when the key or namespace holds the
   separator, and whether it matches the key without regard to case (2.2, 3.4).
3. What `X-Terraform-Get` names for a module in a local repository, and whether it is signed
   with the 90-second token seen on remote downloads (2.4).
4. Whether `versions` lists a version stored as `v0.1.2.zip` with its `v`, and whether
   `download` accepts `0.1.2` for it (2.1, 2.3).
5. Whether module coordinates are matched with or without regard to case (2.2).
6. Where 7.x keeps `module.json` in a local repository and what it holds (2.3).
7. The repository-scoped provider form `/api/terraform/<repo>/v1/providers/` (2.5).
8. The provider registry's `download_url`, `shasums_url` and `shasums_signature_url`, its
   `protocols` when no manifest is deployed, and its `key_id`, `trust_signature`, `source`
   and `source_url` (3.1, 3.2).
9. That the repository's GPG key is `primaryKeyPairRef`; whether `secondaryKeyPairRef`'s key
   is served too; what a PROVIDER repository without a key pair, or with an RSA pair,
   answers; whether 7.x verifies the `.sig` or the zips' SHA-256 against `SHA256SUMS`, and
   when (3.2).
10. Which hashes the mirror sends for a local repository, and whether it reads `:hostname`
    (section 4).
11. The `terraformType` mismatches of section 5: the answers on the wrong registry, a module
    deployed into a PROVIDER repository, a repository without the field, a change on update,
    and a value outside the enum.
12. Properties set on module and provider files (section 9).
13. The statuses and the `{"errors":[...]}` messages 7.x sends on each refusal.
14. Whether `versions` leaves out versions the caller cannot read.

## 15. Versions at the research baseline

| Component | Version |
|---|---|
| Terraform | 1.16.4 (latest 1.16.6), Go 1.26.8 |
| terraform-svchost | v0.2.1 |
| terraform-registry-address | v0.5.0 |
| HashiCorp docs | `hashicorp/web-unified-docs` `f17b63b7b01c`, `v1.16.x` |
| jf | jfrog-cli-artifactory `4a0f3827cfa0` |
| jfrog-client-go | `ab72c67e288c` |
| jfrog-cli-core | `b47892ded3a0` |
| terraform-provider-artifactory | v12.11.14 |

## 16. Artifactory 7.x check

Checked against Artifactory 7.161.32 (SaaS 7.174.0) on 2026-10-19 ([#530](https://github.com/weaveplatform/artifactweave/issues/530)).

**Sources.** The pages named under Sources, read on 2026-10-19, and the release notes:
self-managed 7.84.3 to 7.161.32 ([self-managed releases](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases)),
SaaS to 7.174.0 ([SaaS releases](https://docs.jfrog.com/releases/docs/artifactory-saas-releases)),
and the [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues). The
newest releases, 7.161.32 and 7.174.0, change nothing for Terraform.

**Confirmed for 7.x** (the Terraform page, jf-terraform, calculateterraformindex,
local-repositories, configuration-json-files):
- the module source `<repo>__<ns>/<name>/<provider>` and the deploy path (section 2);
- the provider source `<repo>-rt-ns-<ns>/<type>`, the three provider files, and the GPG key
  pair bound to the repository (section 3);
- the per-repository network mirror at `/api/terraform/<repo>/providers/` (section 4);
- `terraformType` `MODULE` or `PROVIDER` and the layout each applies (section 5);
- `jf terraform publish` and its layout (section 7);
- `POST /api/terraform/{repoKey}/reindex` (section 8).

**Changed or added in 7.x** (release notes from 7.84):
- [7.84.10](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-78410-self-managed)
  (SaaS 7.84.3): OpenTofu clients; remote fixes RTDEV-38770, RTDEV-39600, RTDEV-39948 (P9).
- [7.90.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7905-self-managed):
  a virtual repository with a smart remote in an air-gapped setup answered 404 (RTDEV-40535, P10).
- [7.98.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7987-self-managed):
  concurrent anonymous Terraform requests answered 401 (RTDEV-46304), and remote permissions
  with anonymous access (RTDEV-47642).
- [7.104.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed)
  (SaaS 7.99.1 and 7.103.0): Terraform packages in Cleanup, and Terraform BE packages in
  Cleanup and Archive.
- [7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed):
  virtual resolution across several remotes, and the UI's create and Set Me Up dialogs.
- [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed)
  (SaaS 7.125.3): the provider registry protocol (section 3).
- [7.125.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71257-self-managed):
  `X-Terraform-Get` through a virtual repository (RTDEV-64996, section 2.4).
- 7.125.10: smart remotes resolved from the local instance (RTDEV-69690, P9).
- [7.161.15](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716115-self-managed)
  (7.146.34, SaaS 7.161.13) and the CVE-2026-65924 backports: remote URL hardening (section 11, P9).

None changes what a local module repository stores. 7.125.4 adds the provider registry that
decision 0040 serves.

**Unconfirmed for 7.x:** every **[I]** and **[C]** above; section 14 lists them for
[#216](https://github.com/weaveplatform/artifactweave/issues/216).
