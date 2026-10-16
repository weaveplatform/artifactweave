# npm

Research baseline: **2026-10-16**.

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
- **JFrog's documentation**: `https://docs.jfrog.com/artifactory/docs/npm-repositories`, and the
  knowledge-base article *Why am I receiving a 403 error when attempting to overwrite an npm
  package?* (`jfrog.com/help/r/artifactory-why-am-i-receiving-a-403-error-when-attempting-to-overwrite-an-npm-package`).

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
| Overwriting an existing npm package answers 403 on Artifactory | [D] the knowledge-base article's title; its text was not retrievable |
| Whether that 403 applies to every caller, administrators included, or only to callers without Delete | [I] |
| The status and body Artifactory answers a successful publish, and a refused one | [I] |

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
| The fields Artifactory itself puts in either form, and its `_rev` | [I] |
| `npm ping` is `GET /-/ping`; any 2xx passes and a body that is not JSON is ignored | [S] `lib/utils/ping.js:3-6` |
| `npm whoami` prints the configured username without a request when `.npmrc` holds a username and password; only with a token (or client certificate) does it send `GET /-/whoami` and read `username` | [S] `lib/utils/get-identity.js:6-18` |
| Search is `GET /-/v1/search?text=&size=&from=&quality=&popularity=&maintenance=`, answered with `{objects: [{package: {name, version, description, keywords, date, ...}}], total}` | [D] npm's registry API (research 04) |
| On a local repository Artifactory "calculates the search index asynchronously" after a publish | [D] npm-repositories |
| How many search terms a local repository accepts | [I] |

## 4. Login and tokens

| Fact | Basis |
|---|---|
| `npm login` (`--auth-type=web`, the default) first sends `POST /-/v1/login`; a 4xx or 500 makes npm-profile throw `WebLoginNotSupported` (`ENYI`), and npm then falls back to legacy login | [S] npm-profile `index.js:61-85`; npm `lib/utils/auth.js:38-54` |
| Legacy login is `PUT /-/user/org.couchdb.user:<user>` with `{_id, name, password, type: "user", roles: [], date}`; the answer must carry `token`. On 409 the client reads `GET .../org.couchdb.user:<user>?write=true` and retries with `PUT .../-rev/<rev>`; a 400 is reported as "There is no user with the username" | [S] npm-profile `index.js:139-198` |
| Artifactory lets anonymous requests reach `.*npm/.*/-/user/.*`, so a login needs no prior credentials | [S] `AnonymousNpmLoginInterceptor.java:36` |
| `npm logout` with a token sends `DELETE /-/user/token/<token>` and fails unless the answer is 2xx; with a username and password it sends nothing | [S] `lib/commands/logout.js:25-38` |
| `jf npm` writes `_authToken = <token>` itself when its server config holds an access token; otherwise (a username and password, or npm older than the version it checks) it calls `GET <url>/api/npm/auth` and uses the 200 body verbatim as `.npmrc` lines | [S] `npmcmdutils.go:49-99` |
| What `GET /api/npm/auth` returns (`_auth`, `always-auth`, `email` for a password; `_authToken` for a token), and the scoped `GET /api/npm/<repo>/auth/<scope>` | [I] |
| Whether `/-/npm/v1/tokens` and `/-/npm/v1/user` exist on Artifactory; npm-profile calls them for `npm token` and `npm profile` | [S] npm-profile `index.js:200-233`; Artifactory side [I] |
| Legacy `_auth` is not supported on npm 9 and later, and `npm login` fails for transient users | [D] npm-repositories |

## 5. Storage layout and properties

| Fact | Basis |
|---|---|
| Artifactory's `npm-default` layout is `[orgPath]/[module]/[module]-[baseRev](-[fileItegRev]).tgz`, both integration regexes `.*`, no distinct descriptor | [S] `artifactory.config.xml:151-157` |
| That pattern does not describe the `/-/` paths npm and the JFrog CLI store tarballs at, so layout-based features see npm tarballs only loosely; the npm protocol fixes the path itself | [I] |
| Each package is annotated with `npm.name` and `npm.version` | [D] npm-repositories |
| Artifactory's npm metadata names four property keys, `npm.name`, `npm.version`, `npm.description` and `npm.keywords` | [S] `NpmMetadataProvider.java:52-61` |
| The UI's keyword search matches `npm.keywords` against `*"<keyword>"*`, so the keywords sit in one value with each one quoted; the exact format | [S] `AqlUINpmKeywordsSearchStrategy.java:43`; format [I] |
| Package metadata sits at `.npm/<repo>/<package>`: an include pattern on a permission target must name those paths as well as the package's, or metadata requests through a virtual repository may fail | [D] npm-repositories, "npm Limitations" |
| Whether a local repository's tree shows `.npm/` items to the storage API and AQL | [I] |
| A dist-tag is the multi-valued property `npm.disttag` on the tarball. `jf npm publish --tag <t>` sets it; creating or deleting it, and copying or moving a tarball, make Artifactory recompute the package's tags | [S] jfrog-cli-artifactory `publish.go:33`, `artifactoryupload.go:102-112`; OSS `NpmMetadataInterceptor.java:63-146,201-215` |
| Artifactory validates a `.tgz`'s `package.json` when it is created, whichever API stored it | [S] `NpmMetadataInterceptor.java:72-75` |
| Where Artifactory keeps a deprecation | [I] |

## 6. Strict layout

| Fact | Basis |
|---|---|
| With "Enforce strict layout", packages must sit on the default npm path; `npm install` is served from that path only; an upload elsewhere "is stored but not indexed", and a GET of that path still returns it | [D] npm-repositories |
| New local repositories have it on by default; existing ones are unaffected | [D] npm-repositories |
| Turning it on starts a background conversion that moves each `.tgz` not on its canonical path to that path; downloads keep working | [D] npm-repositories |
| The conversion is `POST /artifactory/api/v1/migrations/npm/repositories/<repo>/migrate`, with `.../resume` for a cancelled or failed one, which carries on with the remaining packages; both need Manage, and only local repositories convert | [D] npm-repositories |
| The strict-layout switch sits under "npm Settings" on the repository's Basic tab, and the repository configuration model in [research 03](03-repository-configuration-model.md#per-ecosystem-summary) holds no npm field for a local repository | [D] npm-repositories |
| The system property `artifactory.npm.enforce.path=true` rejects, on every npm local repository, an upload whose file name does not match the package's own metadata, instead of storing it | [D] npm-repositories |
| The repository JSON field that carries strict layout, and the migrate and resume response bodies | [I] |

## 7. `latest`

| Fact | Basis |
|---|---|
| By default `latest` is the version with the highest SemVer | [D] npm-repositories |
| `artifactory.npm.tag.tagLatestByPublish=true` makes the most recently published version `latest` | [D] npm-repositories |
| Whether a pre-release can become `latest` in a local repository | [I] |

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
| `pkg/layout/bundled.go`: `npm-default` as `([orgPath]/)[module]/-/[module]-[baseRev].[ext]` | Artifactory's definition (section 5); corrected by #264 |

## 10. Inferred and undocumented

Each item below is on #216 for the live comparison in P19:

1. Whether the 403 on overwriting a version applies to every caller, administrators included.
2. The fields Artifactory generates in full and abbreviated packuments, its `_rev`, and what
   a `?write=true` read adds.
3. The statuses and bodies of publish, dist-tag, deprecate and unpublish answers.
4. Where a deprecation is stored, and which permission dist-tag and deprecate need.
5. Whether a local repository's tree shows `.npm/` items.
6. The format of the `npm.keywords` property.
7. The repository JSON field for strict layout, and the migrate and resume response bodies.
8. The body of `GET /api/npm/auth` and of the scoped `/api/npm/<repo>/auth/<scope>`.
9. The status and token type of a legacy login, the token's lifetime, and what
   `DELETE /-/user/token/<token>` answers.
10. Whether a pre-release can become `latest` in a local repository.
11. Whether `GET /-/ping` needs authentication.
12. Whether `/-/npm/v1/tokens` and `/-/npm/v1/user` exist.
13. How many search terms a local repository accepts.
14. Whether the scoped `auth/<scope>` takes the scope with or without its `@`, which npm
    repositories it accepts, and what it answers for a repository that is not npm or does not
    exist.
15. What `api/npm/auth` answers an anonymous caller, and what migrate, resume and reindex
    answer for a repository that is not npm local, a missing one, and a resume with nothing
    to resume.
