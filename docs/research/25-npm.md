# npm

Research baseline: **2026-10-16**.

Checked against: **Artifactory 7.161.32** (self-managed; SaaS 7.174.0) on 2026-10-17, by [#341](https://github.com/weaveplatform/artifactweave/issues/341); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)); [section 11](#11-artifactory-7x-check) records the check.

Phase P7 serves npm packages from local `npm` repositories. [Research 04](04-package-protocols.md)
sketched the protocol and [research 17](17-undocumented-shapes.md) the web login. This report
collects what the npm client, Artifactory and the JFrog CLI do where those leave a point open,
and corrects three facts they got wrong (section 9). Decision [0034](decisions/0034-npm.md)
records the choices.

Each claim is labelled:

| Label | Means |
|---|---|
| **[D]** | documented at the cited URL, read on 2026-10-16 |
| **[S]** | seen in source code at the cited commit |
| **[KB]** | stated in a JFrog knowledge-base article at the cited URL, read on 2026-10-18 |
| **[J]** | recorded in JFrog's public Jira, read on 2026-10-18; it counts as 7.x behaviour under the rules in the [research README](README.md) |
| **[I]** | inferred from the documented facts; not stated by any source |

Sources:
- **The npm CLI** `npm/cli` **v12.2.1** (`ae98c62586b6`): `workspaces/libnpmpublish/lib/publish.js`
  and `unpublish.js`, `lib/commands/{publish,dist-tag,deprecate,ping,whoami,logout}.js` and
  `lib/utils/{auth,ping,get-identity}.js`.
- **npm-profile** **v13.0.1**, `lib/index.js` (login), and **pacote** **v22.0.0**,
  `lib/registry.js` (packument requests).
- **The JFrog CLI**: jfrog-cli-artifactory `4a0f3827cfa0`, `artifactory/commands/npm/`
  (`publish.go`, `artifactoryupload.go`, `npmpublish.go`, `publishstrategy.go`);
  jfrog-cli-core `25dbeb0335c0`, `artifactory/commands/utils/npmcmdutils.go`; build-info-go
  `f9d40441f262`, `build/utils/npm.go`.
- **Artifactory OSS 6.20** (`knottnhupfer/artifactory-oss` at `b99e5649bcad`):
  `base/config/src/main/resources/META-INF/default/artifactory.config.xml`,
  `backend/core/src/main/java/org/artifactory/repo/interceptor/NpmMetadataInterceptor.java`,
  `backend/core/src/main/java/org/artifactory/metadata/service/provider/NpmMetadataProvider.java`,
  `web/rest-ui/src/main/java/org/artifactory/ui/rest/service/artifacts/search/packagesearch/strategy/AqlUINpmKeywordsSearchStrategy.java`,
  `web/application/src/main/java/org/artifactory/webapp/servlet/authentication/interceptor/anonymous/AnonymousNpmLoginInterceptor.java`.
- **JFrog's documentation**: `https://docs.jfrog.com/artifactory/docs/npm-repositories`.
- **JFrog's documentation as the Internet Archive kept it**, for pages that have gone:
  [Authenticate npm using basic authentication](http://web.archive.org/web/20250218151119/https://jfrog.com/help/r/jfrog-artifactory-documentation/authenticate-npm-using-basic-authentication)
  (2025-02-18),
  [Configure npm client using login credentials](http://web.archive.org/web/20250114023657/https://jfrog.com/help/r/jfrog-artifactory-documentation/configure-npm-client-using-login-credentials)
  (2025-01-14) and the REST reference
  [Calculate npm repository metadata](https://web.archive.org/web/20260210113712/https://jfrog.com/help/r/jfrog-rest-apis/calculate-npm-repository-metadata)
  (2026-02-10).
- **JFrog's knowledge-base articles**:
  [000005823](https://jfrog.com/help/r/artifactory-why-am-i-receiving-a-403-error-when-attempting-to-overwrite-an-npm-package)
  (overwriting an npm package),
  [000004147](https://jfrog.com/help/r/how-to-troubleshoot-npm-problems) (troubleshooting npm),
  [000006713](https://jfrog.com/help/r/artifactory-how-to-modify-the-expiration-date-of-the-token-generated-by-the-npm-login-command)
  (the `npm login` token's expiry),
  [000006800](https://jfrog.com/help/r/artifactory-resolving-deployment-failures-to-npm-local-repositories-due-to-policy-configuration)
  (npm deploys refused by policy),
  [000005641](https://jfrog.com/help/r/artifactory-what-does-artifactory-s-recalculate-index-option-do-and-how-do-i-keep-track-of-it)
  (recalculating an index) and
  [000007043](https://jfrog.com/help/r/artifactory-virtual-repository-returns-older-package-version-when-priority-resolution-is-enabled)
  (`latest` in virtual repositories).
- **JFrog's public Jira**, read through `https://jfrog.atlassian.net/rest/api/2/issue/<KEY>`:
  the RTFACT keys cited below.
- **terraform-provider-artifactory** v12.11.14, `docs/resources/local.md`.

## 1. URLs

| Fact | Basis |
|---|---|
| A repository's registry is `<base>/artifactory/api/npm/<repo>/`; the JFrog CLI builds it as `<url>/api/npm/<repo>` | [S] `npmcmdutils.go` `GetNpmRepositoryUrl`; [D] npm-repositories |
| npm addresses a package by its escaped name, so a scoped name arrives as `@scope%2fname` (`spec.escapedName`) | [S] `publish.js:62`, `unpublish.js:35` |
| Tomcat refuses encoded slashes unless configured; Artifactory enables them by default "starting from Artifactory 7.98.9"; behind Apache, `AllowEncodedSlashes NoDecode` | [D] npm-repositories |

## 2. Publishing and changing packages

### 2.1 Publish

| Fact | Basis |
|---|---|
| Publish is **one** `PUT /<escapedName>` (or `POST /-/stage/package/<escapedName>` when staging); the client does not retry on 409 and does not read `?write=true` first | [S] `publish.js:61-66` |
| The body is `{_id, name, description, "dist-tags": {<tag>: <version>}, versions: {<version>: <manifest>}, access, _attachments: {"<name>-<version>.tgz": {content_type: "application/octet-stream", data: <base64>, length}}}` | [S] `publish.js:103-142` |
| The version manifest gets `_id: "<name>@<version>"`, `dist.integrity` (SHA-512, SRI form), `dist.shasum` (SHA-1, hex) and `dist.tarball`, which the client forces to `http://` | [S] `publish.js:118-136` |
| The tarball path the client names is `<name>/-/<name>-<version>.tgz`, so a scoped package's is `@scope/pkg/-/@scope/pkg-<version>.tgz` | [S] `publish.js:118-120`; build-info-go `GetDeployPath` (`npm.go:778-785`) gives the same |
| With provenance, a second attachment, `<name>-<version>.sigstore`, carries the sigstore bundle | [S] `publish.js:145-186` |
| Before publishing, npm 11 and later fetch the packument and refuse a version that already exists, and an implicit `latest` below the highest version, unless `--force` | [S] `lib/commands/publish.js:181-191` |
| Overwriting an existing npm package answers 403 on Artifactory, for every caller, administrators included: JFrog's 7.x request log shows `admin PUT /api/npm/davidfa-npm-local/davidfa-test 403`, and the remedy it gives is to delete the existing package first, "provided you have the appropriate permissions" | [KB] 000005823 |
| A successful publish answers 201 | [J] RTFACT-24072 (JFrog's test log, fixed in 7.22.0): `npm http fetch PUT 201`; [KB] 000004147: `admin PUT /api/npm/npm/test-package HTTP/1.1 201`. artifactweave answers 200 until [#415](https://github.com/weaveplatform/artifactweave/issues/415) |
| From 7.31.10 an npm publish follows the repository's checksum policy, with the SHA-1 in the body's `dist` as the client's checksum, so `server-generated-checksums` does not refuse a mismatch | [J] RTFACT-14280 (fixed in 7.31.10). artifactweave checks `dist` under every policy until [#415](https://github.com/weaveplatform/artifactweave/issues/415). It records `dist.shasum` and the `dist.integrity` hashes it checks as the tarball's original checksums, so under `client-checksums` the tarball's `.sha1` file serves `dist.shasum` ([#399](https://github.com/weaveplatform/artifactweave/issues/399)) |
| The repository's release and snapshot handling applies to npm local deploys: a `.tgz` the policy forbids answers 409 | [KB] 000006800: "409 conflict … due to conflict in the snapshot release handling policy". artifactweave ignores the policy for npm until [#415](https://github.com/weaveplatform/artifactweave/issues/415) |
| The body of a successful publish, and the status and body of one the checksum policy refuses; KB 000005823's log gives the republish 403 a 144-byte body | [I] |

### 2.2 Dist-tags, deprecate and unpublish

| Fact | Basis |
|---|---|
| `npm dist-tag ls` is `GET /-/package/<escapedName>/dist-tags`; `add` is `PUT .../dist-tags/<tag>` with the version as a JSON string; `rm` is `DELETE .../dist-tags/<tag>` | [S] `dist-tag.js:113-117,144-147,197` |
| The client refuses a tag name that is a valid SemVer range, for `dist-tag add` and for `publish --tag` | [S] `dist-tag.js:101-102`, `lib/commands/publish.js:89-90` |
| `npm deprecate` reads `GET /<escapedName>?write=true`, sets `deprecated` on each matching version (an empty message clears it), and sends the whole packument back with `PUT /<escapedName>` and no attachments | [S] `deprecate.js:50-78` |
| `npm unpublish` reads `GET /<escapedName>?write=true` and needs `_rev` | [S] `unpublish.js:36-39` |
| Unpublishing every version (or the only one) is `DELETE /<escapedName>/-rev/<rev>` | [S] `unpublish.js:60-65` |
| Unpublishing one version is `PUT /<escapedName>/-rev/<rev>` with that version removed, a second `GET` for the new `_rev`, then `DELETE <tarball path>/-rev/<rev>`, where the tarball path is `dist.tarball` less the registry's path | [S] `unpublish.js:68-107`, `getPathname` `:12-23` |
| Before that `PUT` the client drops the dist-tags that named the version and, if it was `latest`, points `latest` at the highest remaining version by loose SemVer, prereleases included | [S] `unpublish.js:71-84` |
| The client treats a 404 during unpublish as already done | [S] `unpublish.js:110-115` |
| The statuses and bodies Artifactory answers for dist-tag, deprecate and unpublish requests, and what its `?write=true` read adds | [I] |

## 3. Reading packages

| Fact | Basis |
|---|---|
| pacote asks for the abbreviated packument with `Accept: application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*`, and falls back to the full document on a 404 | [S] `registry.js:15,74,107-111` |
| The abbreviated form carries `name`, `modified`, `dist-tags` and, per version, `dist` (`tarball`, `shasum`, `integrity`, `fileCount`, `unpackedSize`), `deprecated`, `hasInstallScript` and the dependency fields; the full form adds `_id`, `_rev`, `time`, `readme`, `maintainers` and the rest of each manifest | [D] npm's `package-metadata.md` (research 04) |
| A 7.x full packument from a local repository carries `"_rev" : "1-0"`, `"users" : { }`, `time` with `"unpublished" : null` beside `created`, `modified` and one entry per version, and the latest version's `author` and `license` at the top level. Each version's `dist` has only `tarball`, `shasum` and `integrity`, and there is no `readme` or `maintainers` | [J] RTFACT-30472 (JFrog's reproduction, 2024, two packuments quoted in full), RTFACT-29708 (7.55.14). artifactweave differs until [#416](https://github.com/weaveplatform/artifactweave/issues/416) |
| Both captures are of packages with one version; the `_rev` of a package with more, the fields of the abbreviated form, and what a `?write=true` read adds | [I] |
| Packuments carry `X-Checksum-Sha1` and an `ETag` equal to it, and a matching `If-None-Match` answers 304 | [J] RTFACT-30566 (JFrog, 7.87.3), RTFACT-19423 (fixed 2019); both captures are of remote or virtual repositories. artifactweave sends neither until [#416](https://github.com/weaveplatform/artifactweave/issues/416) |
| `npm ping` is `GET /-/ping`; any 2xx passes and a body that is not JSON is ignored | [S] `lib/utils/ping.js:3-6` |
| `npm whoami` prints the configured username without a request when `.npmrc` holds a username and password; only with a token (or client certificate) does it send `GET /-/whoami` and read `username` | [S] `lib/utils/get-identity.js:6-18` |
| Search is `GET /-/v1/search?text=&size=&from=&quality=&popularity=&maintenance=`, answered with `{objects: [{package: {name, version, description, keywords, date, ...}}], total}` | [D] npm's registry API (research 04) |
| On a local repository Artifactory "calculates the search index asynchronously" after a publish | [D] npm-repositories |
| A local repository matches up to three search terms | [D] [7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed), "Improved npm Search" |
| A local repository's search answered `{"objects":[{"package":{"name","description","maintainers","version","date","keywords","author"}}]}` on SaaS 7.27.9, with no `scope`, `links`, `score`, `searchScore`, `total` or `time` | [J] RTFACT-26660 (JFrog's reproduction). Decision 0034's planned body differs; [#291](https://github.com/weaveplatform/artifactweave/issues/291) settles it |

**Maintainer decision (2026-10-18).** artifactweave answers `_rev` as `"1-0"`, as 7.x does
([#416](https://github.com/weaveplatform/artifactweave/issues/416)), and drops the
stale-`_rev` 409 that decision 0034 planned for unpublish
([#290](https://github.com/weaveplatform/artifactweave/issues/290)). A constant `_rev` cannot
show that a client's view is stale, so 7.x cannot refuse one either ([J] RTFACT-30472,
RTFACT-29708).

## 4. Login and tokens

| Fact | Basis |
|---|---|
| `npm login` (`--auth-type=web`, the default) first sends `POST /-/v1/login`; a 4xx or 500 makes npm-profile throw `WebLoginNotSupported` (`ENYI`), and npm then falls back to legacy login | [S] npm-profile `index.js:61-85`; npm `lib/utils/auth.js:38-54` |
| Legacy login is `PUT /-/user/org.couchdb.user:<user>` with `{_id, name, password, type: "user", roles: [], date}`; the answer must carry `token`. On 409 the client reads `GET .../org.couchdb.user:<user>?write=true` and retries with `PUT .../-rev/<rev>`; a 400 is reported as "There is no user with the username" | [S] npm-profile `index.js:139-198` |
| Artifactory lets anonymous requests reach `.*npm/.*/-/user/.*`, so a login needs no prior credentials. A legacy login answers 201: a 7.x request log shows `anonymous PUT /api/npm/npm/-/user/org.couchdb.user:admin HTTP/1.1 201` | [S] `AnonymousNpmLoginInterceptor.java:36`; [KB] 000004147 |
| A token from `npm login` lasts one month (2592000 s) by default. Only the global system property `artifactory.artifactory.tokens.expiration.timeSecs` changes it | [KB] 000006713; [J] RTFACT-30505 (7.55.8) |
| 7.x serves web login, `POST /-/v1/login` answering `loginUrl` and `doneUrl`, from about 7.52, and JFrog's npm page logs in with `npm login --auth-type=web` | [J] RTFACT-29293 (Done, 2023-01), RTFACT-29487: "Artifactory version 7.52.x and above … will automatically direct the user to web based login"; [D] npm-repositories. artifactweave answers 405 until [#262](https://github.com/weaveplatform/artifactweave/issues/262) |
| `npm logout` with a token sends `DELETE /-/user/token/<token>` and fails unless the answer is 2xx; with a username and password it sends nothing | [S] `lib/commands/logout.js:25-38` |
| `jf npm` writes `_authToken = <token>` itself when its server config holds an access token; otherwise (a username and password, or npm older than the version it checks) it calls `GET <url>/api/npm/auth` and uses the 200 body verbatim as `.npmrc` lines | [S] `npmcmdutils.go:49-99` |
| `GET /api/npm/auth` no longer returns `always-auth` or `email`, "unnecessary and obsolete npm configuration fields", in its answer or in generated `.npmrc` snippets | [D] [7.161.26](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-716126-self-managed) (RTDEV-96526), [SaaS 7.167.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71670-saas) |
| For Basic credentials the answer's `_auth` is the base64 of `<user>:<encrypted password>`, not of the password sent: JFrog's example `_auth = YWRtaW46e0RFU2VkZX1uOFRaaXh1Y0t3bHN4c2RCTVIwNjF3PT0=` decodes to `admin:{DESede}n8TZ…`, followed by the `email` and `always-auth = true` lines that 7.161.26 dropped | [D] archived *Authenticate npm using basic authentication* (2025-02-18). Decision 0034 differs; [#292](https://github.com/weaveplatform/artifactweave/issues/292) settles it |
| The scoped `GET /api/npm/<repo>/auth/<scope>` takes the scope without its `@` (`auth/jfrog` answers for `@jfrog`). It answers `@<scope>:registry=<registry URL>`, then `//<host>/artifactory/api/npm/<repo>/:_password=<base64>` and `:username=<user>` lines, plus `:email=` and `:always-auth=true` lines before 7.161.26 | [D] archived *Configure npm client using login credentials* (2025-01-14); [J] RTFACT-30181 (`…/auth/testreferencetoken`). Decision 0034 differs; [#292](https://github.com/weaveplatform/artifactweave/issues/292) settles it |
| Both auth operations answer errors in the Artifactory envelope, such as `{"errors":[{"status":400,"message":"Not supported user type."}]}` on 7.73.0 | [J] RTFACT-30181 (JFrog's reproduction, Done), RTFACT-21308, RTFACT-8180, RTFACT-19005. artifactweave answers npm's envelope until [#292](https://github.com/weaveplatform/artifactweave/issues/292) |
| What `GET /api/npm/auth` answers a Bearer token | [I] |
| Whether `/-/npm/v1/tokens` and `/-/npm/v1/user` exist on Artifactory; npm-profile calls them for `npm token` and `npm profile` | [S] npm-profile `index.js:200-233`; Artifactory side [I] |
| Legacy `_auth` is not supported on npm 9 and later, and `npm login` fails for transient users | [D] npm-repositories |

## 5. Storage layout and properties

| Fact | Basis |
|---|---|
| Artifactory 7.x's `npm-default` layout is `[orgPath]/-/[module]-[baseRev](-[fileItegRev]).tgz`, both integration regexes `.*`, no distinct descriptor; 6.20's was `[orgPath]/[module]/[module]-[baseRev](-[fileItegRev]).tgz` | [J] RTFACT-21456 (fixed in 7.5.0); [S] JFrog's 7.38.8 export in jfrog-cli-artifactory `4a0f3827cfa0` ([research 03](03-repository-configuration-model.md#bundled-definitions)); [S] 6.20 `artifactory.config.xml:151-157` |
| 7.x's pattern reads `<name>/-/<name>-<version>.tgz` as organisation and module `<name>`, and matches no scoped tarball, since `[module]` stops at a slash. artifactweave's adds an optional scope group, so `@scope/name/-/@scope/name-<version>.tgz` reads as module `name` of organisation `@scope/name` (DV-100). The npm format derives its paths from `package.json`, not from the layout | [S] `pkg/layout/bundled.go`; [I] for what 7.x does with a scoped tarball |
| Each package is annotated with `npm.name` and `npm.version` | [D] npm-repositories |
| Artifactory's npm metadata names four property keys, `npm.name`, `npm.version`, `npm.description` and `npm.keywords`, and 7.x still writes them when it indexes a tarball | [S] `NpmMetadataProvider.java:52-61`; [J] RTFACT-30834 (JFrog, 2024, a 7.x log), RTFACT-30153 (AQL on `@npm.keywords` and `@npm.description`) |
| `npm.keywords` is one value, the keywords as a JSON array of strings: a 7.x log writes `npm.keywords=[["cli","parser",…]]`. The UI's keyword search matches it against `*"<keyword>"*` | [J] RTFACT-30834; [S] `AqlUINpmKeywordsSearchStrategy.java:43` |
| Indexing also writes `artifactory.licenses` from `package.json`'s `license`: "Writing metadata attributes:npm.version=[2.0.1],artifactory.licenses=[Python-2.0],…" | [J] RTFACT-30834. artifactweave does not until [#416](https://github.com/weaveplatform/artifactweave/issues/416) |
| A local repository writes each package's metadata to `.npm/<package>/package.json`: a 7.x reindex logs "write npm package metadata for repo 'npm-local' on path '.npm/lodash/package.json'" | [KB] 000005641 |
| Package metadata sits at `.npm/<repo>/<package>`: an include pattern on a permission target must name those paths as well as the package's, or metadata requests through a virtual repository may fail | [D] npm-repositories, "npm Limitations" |
| A copy or move to a path under `.npm/` is refused, so source metadata cannot overwrite the destination's generated files | [D] [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed), "npm metadata protection" |
| A local repository can sign each version with an ECDSA key pair (the repository's `primaryKeyPairRef` and `secondaryKeyPairRef`): the version's `dist.signatures` holds `keyid` and `sig`, and `npm audit signatures` checks them against `GET /-/npm/v1/keys` | [D] npm-repositories, "Use npm Audit"; [research 04](04-package-protocols.md#url-layout-1) |
| Whether a local repository's tree shows `.npm/` items to the storage API and AQL | [I] |
| A dist-tag is the multi-valued property `npm.disttag` on the tarball. `jf npm publish --tag <t>` sets it; creating or deleting it, and copying or moving a tarball, make Artifactory recompute the package's tags | [S] jfrog-cli-artifactory `publish.go:33`, `artifactoryupload.go:102-112`; OSS `NpmMetadataInterceptor.java:63-146,201-215` |
| Artifactory validates a `.tgz`'s `package.json` when it is created, whichever API stored it | [S] `NpmMetadataInterceptor.java:72-75` |
| `npm deprecate` sets the `npm.deprecated` property on the version's tarball, and the packument then shows the message | [J] RTFACT-30928 (JFrog's reproduction, SaaS 7.101.0): "The npm deprecate command adds the property (npm.deprecated)"; RTFACT-14726 (JFrog's comment, 2022-10-31): "The NPM metadata and Artifactory Properties both get updated" |

## 6. Strict layout

| Fact | Basis |
|---|---|
| With "Enforce strict layout", packages must sit on the default npm path; `npm install` is served from that path only; an upload elsewhere "is stored but not indexed", and a GET of that path still returns it | [D] npm-repositories |
| New local repositories have it on by default; existing ones are unaffected | [D] npm-repositories |
| Turning it on starts a background conversion that moves each `.tgz` not on its canonical path to that path; downloads keep working | [D] npm-repositories |
| The conversion is `POST /artifactory/api/v1/migrations/npm/repositories/<repo>/migrate`, with `.../resume` for a cancelled or failed one, which carries on with the remaining packages; both need Manage, and only local repositories convert | [D] npm-repositories |
| The strict-layout switch sits under "npm Settings" on the repository's Basic tab, and the repository configuration model in [research 03](03-repository-configuration-model.md#per-ecosystem-summary) holds no npm field for a local repository | [D] npm-repositories |
| The system property `artifactory.npm.enforce.path=true` rejects, on every npm local repository, an upload whose file name does not match the package's own metadata, instead of storing it | [D] npm-repositories |
| The REST repository schema and the Terraform provider have no strict-layout field | [D] [createrepository](https://docs.jfrog.com/artifactory/reference/createrepository.md), [getrepositoryconfigurationv2](https://docs.jfrog.com/artifactory/reference/getrepositoryconfigurationv2.md); [S] terraform-provider-artifactory v12.11.14 `docs/resources/local.md` |
| Without strict layout, 7.x indexes a tarball off its canonical path: a 2024 log indexes `testfolder1/-/argparse-2.0.1.tgz` | [J] RTFACT-30834 |
| The repository JSON field that carries strict layout, if one exists, and the migrate and resume response bodies | [I] |
| `POST /api/npm/<repo>/reindex` needs only Manage on the repository: "From version 4.8 only requires the set of permissions assumed by Manage" | [D] archived *Calculate npm repository metadata* (2026-02-10). artifactweave requires an administrator until [#293](https://github.com/weaveplatform/artifactweave/issues/293) |
| A reindex is scheduled, not run before the answer: it answers `Recalculating index for npm repository npm-local scheduled to run` (`application/text`), and the log reports the run | [D] archived *Calculate npm repository metadata*, sample output; [KB] 000005641. artifactweave rebuilds before answering until [#293](https://github.com/weaveplatform/artifactweave/issues/293) |
| The switch was first listed in [SaaS 7.170.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71700-saas), which turned it on for new local repositories; the self-managed notes up to 7.161.32 do not list it, so whether 7.161.32 has it is open | [D] SaaS notes; self-managed [I] |
| An upload that breaks `artifactory.npm.enforce.path` is refused; letting it through was a bug | [D] [7.146.22](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-714622-self-managed) (RTDEV-86877) |

## 7. `latest`

| Fact | Basis |
|---|---|
| By default `latest` is the version with the highest SemVer | [D] npm-repositories |
| `artifactory.npm.tag.tagLatestByPublish=true` makes the most recently published version `latest` | [D] npm-repositories |
| `tagLatestByPublish` is one global system property, off by default. Artifactory ignored it until 6.18.0 and 7.3.0, and a per-repository setting is an open request | [J] RTFACT-20127 (fixed in 6.18.0 and 7.3.0), RTFACT-16406 (open) |
| Artifactory stores `latest` as it stores any dist-tag, as `npm.disttag=latest` on the latest version's tarball, and indexing rewrites it | [D] [7.84.10](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-78410-self-managed), RTDEV-37593 and RTDEV-40221; [J] RTFACT-31340, RTFACT-21400, RTFACT-30153 (an AQL query on `{"@npm.disttag": "latest"}`). artifactweave stores it only for a pin until [#289](https://github.com/weaveplatform/artifactweave/issues/289) |
| `npm publish --tag <t>` leaves `latest` where it is, unless the package has no version yet | [J] RTFACT-14111, RTFACT-14920, RTFACT-31108; RTFACT-24072, JFrog's reproduction: after `npm publish --tag beta` of 1.0.0-beta.2, `latest` stays 1.0.0-beta.1. artifactweave lets a tagged publish of a higher stable version become `latest` until [#289](https://github.com/weaveplatform/artifactweave/issues/289) |
| The `latest` a publish body names is ignored, and `npm dist-tag add <version> latest` sets it | [J] RTFACT-8184, RTFACT-26099; RTFACT-16009, RTFACT-7400 |
| In a local repository a pre-release does not become `latest` while a stable version exists: with 1.0.0 and 1.0.1-2 published, `latest` is 1.0.0 | [J] RTFACT-30783 (a local repository on 7.94.1), RTFACT-17732 (6.5.1) |
| Virtual repositories merge `latest` by the same stable-first rule from 7.120.0; the release notes do not list it | [KB] 000007043 (virtual repositories only) |
| In a virtual repository a custom dist-tag resolves to the first member, in resolution order, that has it; `artifactory.npm.merge.latest.dist.tag.base.strategy.enabled=true` (from 7.75.3) takes the highest SemVer version across members instead, or the newest with `tagLatestByPublish` | [D] npm-repositories, "Configure Distribution Tag Behavior" |

## 8. What the JFrog CLI sends

| Fact | Basis |
|---|---|
| `jf npm publish` by default runs `npm pack`, then uploads the tarball with an ordinary REST deploy, `PUT <repo>/<deploy path>;<props>`, carrying the build properties and `npm.disttag` when `--tag` is given | [S] `artifactoryupload.go:44-112` |
| With `--run-native` it runs `npm publish`, then searches for the tarball and sets the build properties on it | [S] `npmpublish.go:59-100`, `publishstrategy.go:24-28` |
| Build-info dependencies come from npm's cache, keyed by `dist.integrity`, so packuments must carry the SHA-512 integrity | [S] build-info-go `npm.go` |

## 9. Corrections to earlier research

| Was | Now |
|---|---|
| Research 04: "On 409 the client fetches the packument with `?write=true` and retries" | Publish is one `PUT`; there is no retry (section 2.1) |
| Research 04: scoped tarballs at `@scope/name/-/name-<ver>.tgz` | `@scope/name/-/@scope/name-<ver>.tgz` (section 2.1) |
| Research 17: "npm 9 and later refuse registries without web login unless `--auth-type=legacy`" | npm 12 falls back to legacy login when the web-login start answers 4xx or 500 (section 4) |
| `pkg/layout/bundled.go`: `npm-default` as `([orgPath]/)[module]/-/[module]-[baseRev].[ext]` | 7.x's definition with a scope group (section 5); corrected by #264 and #376 |

## 10. Inferred and undocumented

Each item below is on #216 for the live comparison in P19:

1. Settled by the audit ([#378](https://github.com/weaveplatform/artifactweave/issues/378)):
   the 403 on overwriting a version applies to every caller, administrators included ([KB]
   000005823, section 2.1).
2. The fields of the abbreviated packument, the `_rev` of a package with more than one
   version, and what a `?write=true` read adds. The audit settled the full packument's fields
   and `_rev` `1-0` ([J] RTFACT-30472, RTFACT-29708, section 3).
3. The statuses and bodies of publish, dist-tag, deprecate and unpublish answers, apart from a
   successful publish's 201, which the audit settled (section 2.1).
4. Which permission dist-tag and deprecate need. The audit settled where a deprecation is
   stored: the `npm.deprecated` property ([J] RTFACT-30928, RTFACT-14726, section 5).
5. Whether a local repository's `.npm/` items show to the storage API and AQL. The audit
   found that 7.x writes `.npm/<package>/package.json` there ([KB] 000005641, section 5).
6. Settled by the audit: `npm.keywords` is one value holding a JSON array of strings ([J]
   RTFACT-30834, section 5).
7. The repository JSON field for strict layout, and the migrate and resume response bodies.
   The REST schema and the Terraform provider show no such field (section 6).
8. What `GET /api/npm/auth` answers a Bearer token. The audit settled the Basic answer and the
   scoped `/api/npm/<repo>/auth/<scope>` answer from JFrog's archived 7.x documentation
   (section 4).
9. The token type of a legacy login, and what `DELETE /-/user/token/<token>` answers. The
   audit settled that a legacy login answers 201, to an anonymous request too ([KB]
   000004147), and that its token lasts 30 days by default ([KB] 000006713, section 4).
10. Settled by the audit: in a local repository a pre-release does not become `latest` while
    a stable version exists ([J] RTFACT-30783, RTFACT-17732, section 7).
11. Whether `GET /-/ping` needs authentication.
12. Whether `/-/npm/v1/tokens` and `/-/npm/v1/user` exist.
13. Settled by the 7.x check: up to three search terms (section 3).
14. Which npm repositories the scoped `auth/<scope>` accepts, and what it answers for a
    repository that is not npm or does not exist. The audit settled that it takes the scope
    without its `@` (section 4).
15. What `api/npm/auth` answers an anonymous caller, and what migrate, resume and reindex
    answer for a repository that is not npm local, a missing one, and a resume with nothing
    to resume.
16. Whether self-managed 7.161.32 already has the strict-layout switch (section 6). Without
    it, 7.x indexes a tarball off its canonical path ([J] RTFACT-30834).
17. What 7.x answers a copy or move into `.npm/` (section 5).

## 11. Artifactory 7.x check

Checked against Artifactory 7.161.32 on 2026-10-17 ([#341](https://github.com/weaveplatform/artifactweave/issues/341)); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

**Sources.** JFrog's current documentation, read on 2026-10-17 (the pages named below), and the Artifactory release notes: self-managed 7.84.3 to 7.161.32 ([self-managed releases](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases)), SaaS 7.80.0 to 7.174.0 ([SaaS releases](https://docs.jfrog.com/releases/docs/artifactory-saas-releases)), the [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues) and the [deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations). JFrog no longer publishes the notes for 7.0 to 7.83 (the [end-of-life page](https://docs.jfrog.com/releases/docs/artifactory-end-of-life) keeps only their dates), so a change in that range shows only through the current documentation. The npm pages read are [npm-repositories](https://docs.jfrog.com/artifactory/docs/npm-repositories.md) and [local-repositories](https://docs.jfrog.com/artifactory/docs/local-repositories.md). The audit of 2026-10-18 added JFrog's KB articles, its public Jira, the JFrog clients' sources and archived JFrog pages, listed at the top of this report.

**Confirmed for 7.x:**
- Strict layout, its default for new repositories, the conversion API and its Manage
  requirement, and `artifactory.npm.enforce.path` (section 6).
- `latest` by highest SemVer and `tagLatestByPublish` (section 7).
- The asynchronous search index (section 3).
- The `npm.name` and `npm.version` properties (section 5).
- `@scope%2fname`: encoded slashes are allowed by default from 7.98.9 (npm-repositories, "npm
  Scope Packages").
- Distinct response headers for the full and the abbreviated packument ([7.90.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7905-self-managed),
  RTDEV-43447).
- npm `deprecate` firing webhooks ([7.104.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71045-self-managed)).
- From the audit: the republish 403 for administrators too (section 2.1); legacy login's 201
  to an anonymous request, its 30-day token and the scope given without `@` (section 4);
  `npm.deprecated` and the `npm.keywords` format (section 5); and stable-first `latest` in a
  local repository (section 7).

**7.x differs from the code on main** where the tables above name an issue; [decision
0034's 7.x check](decisions/0034-npm.md#artifactory-7x-check) lists each difference with the
issue that fixes it.

**Changed or added in 7.x:**
- Search matches up to three terms (section 3; decision 0034 already did).
- `GET /api/npm/auth` drops `always-auth` and `email` (section 4; decision 0034 amended, posted
  on [#292](https://github.com/weaveplatform/artifactweave/issues/292)).
- Copy and move into `.npm/` are refused (section 5; DV-042 notes it).
- ECDSA package signing with `dist.signatures` (section 5): artifactweave signs nothing, and
  decision 0034 leaves it out of P7.
- Custom dist-tags in virtual repositories (section 7): an input for P10.
- In remote and virtual repositories, a proxied boolean `deprecated` keeps its type rather
  than becoming the string `"false"` ([7.111.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71114-self-managed),
  [SaaS 7.107.1](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71071-saas),
  RTDEV-53104; [J] RTFACT-31013): an input for P9 and P10. It says nothing about a local
  repository, whose `deprecated` is the `npm.deprecated` message (section 5).
- pnpm and Yarn Modern support ([7.133.3](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71333-self-managed)), npm audit on remote repositories
  ([7.125.4](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71254-self-managed)), npm provenance and attestations in remote and virtual repositories
  ([7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed)), FHIR remotes ([SaaS 7.166.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71660-saas)) and a two-hour default metadata
  retrieval cache for npm virtual repositories ([SaaS 7.165.0](https://docs.jfrog.com/releases/docs/artifactory-saas-releases#artifactory-71650-saas)): inputs for P9 and P10.

**Unconfirmed for 7.x:** the **[S]** claims from the 6.20 source on `NpmMetadataInterceptor`'s
property following and `package.json` validation. They stay on
[#216](https://github.com/weaveplatform/artifactweave/issues/216), with items 16 and 17 of
section 10 added. The audit confirmed the other two 6.20 claims: the four `npm.*` property
keys and the keyword format ([J] RTFACT-30834), and anonymous access to `-/user/` ([KB]
000004147).
