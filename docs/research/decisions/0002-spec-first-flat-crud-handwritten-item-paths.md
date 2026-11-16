# 0002: Spec-first for flat CRUD, hand-written handlers for item paths

Status: Proposed

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on
2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

## Context

hostweave established spec-first OpenAPI on chi with oapi-codegen strict servers
(hostweave decision 0012). artifactweave's management surface is a good fit for that
pattern, but much of Artifactory's API is not: item paths span many segments
(`/api/storage/{repo}/{path...}`), one operation changes its response shape on a query
flag (`?list`, `?stats`, `?properties`, `?permissions`), bodies arrive as vendor media
types (`application/vnd.org.jfrog.artifactory.*+json`), `text/plain` and
`application/yaml`, and downloads and uploads stream. OpenAPI 3.0 path parameters cannot
span segments and oapi-codegen emits single-segment chi patterns.

Clients also hard-check status codes that do not follow REST convention: repository
create, update and delete return 200, a legacy group PUT returns 201, property writes
return 204, a folder PUT returns 201.

## Decision

**Seven specs, strict servers for the flat families only.** One OpenAPI 3.0.3 spec per
service, bundled from `api/src/<service>/`: `api/artifactory.openapi.yaml`,
`api/access.openapi.yaml` and `api/event.openapi.yaml`, and since P1b
`api/distribution.openapi.yaml`, `api/pfed.openapi.yaml`, `api/grid.openapi.yaml` and
`api/worker.openapi.yaml`. Each generates an oapi-codegen v2.8 strict chi server into
`internal/platform/gen/<service>` and a Go client into `pkg/client/<service>`. The strict
servers cover the families whose operations have single-segment parameters and one
response shape: repositories, the legacy security shims, Access v2 users, groups and
permissions, tokens, roles, environments, projects, webhooks, cleanup policies and system.

**Hand-written handlers for item paths.** Operations with multi-segment path parameters or
mode-switch responses (artifact deploy and download, storage item info, properties, copy,
move, zap, archive download, trash, replication status, Docker and the other package
protocols, metadata PATCH) are chi handlers in `internal/artifacts/adapters/http` and
`internal/artifacts/ecosystem/*`, mounted before the generated mux. They stay in the spec
for documentation with the extension `x-handwritten: true`; a test asserts that the
generated router never claims a path marked that way.

**A HEAD of a GET-only route.** The strict servers mount only the methods each spec
declares, and chi routes a HEAD only to a route that registers one, so a HEAD of a
GET-only operation answered 405. 7.x serves its REST API with Jersey, which hands a HEAD
to the resource's `@HEAD` method or, when there is none, to its `@GET` method and leaves
the entity out (JAX-RS 2.1, section 3.3.5). In 7.161.26 only four resources declare a
`@HEAD` of their own: `SecurityResourceV2`'s permission target check, which the spec
declares as `HEAD`, `ReleaseBundleResource`, `CheckupResource` and the UI's
`UserApiKeyResource`. So the router answers a HEAD of every generated GET, and of the
GitLab subgroup forms of the generated VCS operations (`VCSAPI.VCSNested`), through that
GET (`middleware.HeadAsGet`, [#1598](https://github.com/weaveplatform/artifactweave/issues/1598)):
the GET's status and header fields, its `Content-Length`, and no body.

The hand-written GET routes answer a HEAD the same way
([#1613](https://github.com/weaveplatform/artifactweave/issues/1613)), as `handwrittenHeads`
in `internal/app/head.go` lists them. 7.161.26's `web.xml` maps `/api/*` to Jersey's
`ArtifactoryRestServlet` and to no other servlet, and `RepoFilter` passes every API path
on to it, so each of them is a Jersey resource method too: `ArtifactResource` (the storage
API), `PingResource`, `ConfigResource`, `ArchiveResource`, `DownloadResource`,
`ReplicationResource` and `VersionsResources` in the open-source tree, each with a `@GET`
alone, and the resources of the package add-ons, which are not in that tree but which
`ShadowResponseFilter`, a Jersey response filter, shadows by package type, the Terraform
backend's among them **[I]**. Access is a Jersey application as well (the stack trace in
RTFACT-16596 runs `org.jfrog.access.server.rest.resource` methods through Jersey). A
GET route that neither `handwrittenHeads` nor `headUnserved` names fails a unit test.

- The request is the GET for everything ahead of routing, so the key gate, admission,
  authentication's public routes and the API restriction check decide it as they decide
  the GET ([decision 0043](0043-api-reliability.md#cost-classes-and-how-admission-finds-them)).
  A generated handler, and a hand-written download (a folder archive, `api/download`, a
  skill bundle), sees a HEAD again, so that a file download ignores `Range`, counts no
  delivery, and copies no file it has declared the length of.
- Any other hand-written handler is written for the GET alone, and runs the HEAD as the
  GET (`HeadAsGet.RunsGet`), as Jersey runs the `@GET`: the HEAD sends the length of the
  body the handler writes. A handler with a HEAD branch of its own, such as Pub's and
  Cargo's JSON writers, would skip its body and send a length of 0. None of these
  handlers counts a download.
- A folder archive's HEAD builds no archive, since the GET streams it, and goes without a
  `Content-Length`, which only building the archive would tell
  (`middleware.HeadLengthUnknown`) **[I]**.
- A HEAD route keeps the request where it matches the path as closely: a spec `HEAD`,
  or a hand-written route's. JAX-RS picks the path before the method (section 3.7.2),
  the template with the most literal characters first, where chi picks the method
  first and backtracks into a parameter to find a HEAD route. So
  `HEAD /distribution/api/v1/release_bundle/{name}/LATEST` answers through the GET of
  that path, not through the HEAD of `.../{release_bundle_name}/{release_bundle_version}`,
  `HEAD /artifactory/api/npm/auth` through the GET of npm's authentication, not
  through the npm protocol's `HEAD /api/npm/{repoKey}`, and so do npm's `-/ping`,
  `-/whoami`, `-/v1/search` and `-/package/` routes and LuaRocks' `api/tool_version` and
  `check_rockspec`. The PyPI JSON API is the exception the other way: the PyPI
  protocol's own HEAD route answers it, with the GET's status.
- The JFrog Router's probes (`/router/api/v1/system/health`, `readiness`, `liveness`)
  still answer a HEAD 405. The Router is a Go service built on Traefik (its `traefik`
  settings in RTFACT-25374, Traefik's access log in RTFACT-31079), not a Jersey
  application, and JFrog's load-balancer page documents only a GET of them **[I]**.
- **[I]** No capture of a HEAD on any of these routes is known, so the headers 7.x sends
  are inferred, `Content-Length` among them. 7.161.26's `JerseyApplication` sets
  `CommonProperties.OUTBOUND_CONTENT_LENGTH_BUFFER` to -1, which turns Jersey's own
  length buffering off, so a length 7.x sends comes from the resource or from Tomcat.
  artifactweave sends the GET's length, as RFC 9110 section 8.6 allows on a HEAD.

**Content-type normalisation.** Middleware rewrites `application/vnd.org.jfrog.artifactory.*+json`
to `application/json` before the strict decoder, and accepts `application/yaml` on the
configuration PATCH.

**Status-code contract.** The research inventories carry a status-code column per endpoint
taken from the JFrog docs and from the JFrog CLI and Terraform provider sources. Handlers
take their codes from that table, not from convention.

**Error response profiles.** One table in `internal/platform/httpkit/apierror`, keyed by
profile. A profile selects the error envelope, response media type, and default
authentication challenge. Several API surfaces can share a profile; JFrog Distribution and
OCI registries have distinct profiles.

| Error response profile | Envelope |
|---|---|
| Artifactory REST | `{"errors":[{"status":<int>,"message":"<string>"}]}` |
| Access, Projects | `{"errors":[{"code":"<string>","message":"<string>"}]}` |
| Event (`/event/`), Workers (`/worker/`) | as Access; JFrog documents no Workers error body |
| JFrog Distribution (`/distribution/`) | `{"status_code":<int>,"message":"<string>","errors":[{"status":<int>,"message":"<string>"}]}`, with `detail` when set |
| Cold-storage retention (`/api/retention/`) | the Artifactory envelope plus top-level `error` (the status text) and `message` |
| Import and export (`/api/import/`, `/api/export/`, `/api/artifactexport/`) | the Artifactory envelope plus top-level `error`, which carries the message |
| Platform federation (`/pfed/`) | `{"code":<int>,"message":"<string>"}`; `code` is the HTTP status |
| Grid (`/grid/`) | `{"code":"<CODE>","message":"<string>"}`; `details` when set |
| OCI `/v2/` | `{"errors":[{"code","message","detail"}]}` with the OCI spec codes |
| Xray (stub) | `{"error":"<string>"}` |
| npm | `{"error":"<string>"}` |
| Terraform registry | `{"errors":["<string>"]}` |
| Go proxy, PyPI | plain text |
| NuGet | text or XML as the NuGet client expects |

The `/pfed` row follows the `ErrorResponse` every `/pfed` page documents, an integer `code`
([Add Follower JPD](https://docs.jfrog.com/administration/reference/addfollowerjpd.md)), and
the import and export row the one their pages document, `errors[]` beside `error`, the
"simple error message (legacy format)", with no top-level `message`
([Export Repository with Metadata](https://docs.jfrog.com/administration/reference/exportrepositorywithmetadata.md)).

**Base URL resolver.** Embedded absolute URLs (npm `dist.tarball`, Helm `urls`, NuGet
`@id`, Terraform `download_url`, Docker `Location` and `WWW-Authenticate` realm,
file-info `uri` and `downloadUri`, repository `url`) come from one resolver in
`internal/platform/httpkit/baseurl` with Artifactory's precedence (OSS 6.20
`HttpUtils.getServletContextUrl`, research 17; the 7.x
[HTTP settings](https://docs.jfrog.com/installation/docs/http-settings) page documents the
`X-JFrog-Override-Base-Url` header and the Custom Base URL but not their precedence, so the
order is **[I]**, unconfirmed for 7.x): `X-JFrog-Override-Base-Url`, then the
configured `base_url` (Artifactory's `urlBase`), then `X-Forwarded-Proto`, `X-Forwarded-Host`
and `X-Forwarded-Port`, then the request. A configured `base_url` is the operator naming the
public URL, so no header the operator does not control overrides it; before #313 the forwarded
headers came first, which let a client that could set `X-Forwarded-Host` choose the Docker
token realm other clients send credentials to. A middleware puts the resolved base into the
request context. Each `X-Forwarded-*` header can be a comma-separated list, one entry per
proxy; the resolver takes the first entry, the client-facing hop, ignores an empty first
entry, and accepts only `http` or `https` as the forwarded scheme and a number as the
forwarded port (#249, research 17). The Docker subdomain method still reads the host the
client addressed (the first `X-Forwarded-Host` entry, else `Host`) to pick the repository,
but the URLs it answers with take their host from the resolved base URL. The legacy
`X-Artifactory-Override-Base-Url` is not honoured (DV-037), and a configured `base_url` is
used as written rather than with the request's scheme (DV-038).

**Wire models and the linter.** JFrog's JSON mixes camelCase, snake_case and kebab-case,
sometimes inside one struct, so per-package case rules cannot express it. The wire models
live only in `internal/artifacts/api/repo` and `internal/kernel/wire`, and those
packages are excluded from `tagliatelle`; everything else keeps the template rule.

```yaml
linters:
  settings:
    tagliatelle:
      case:
        rules:
          json: snake
          yaml: snake
        overrides:
          - pkg: internal/artifacts/api/repo
            ignore: true
          - pkg: internal/kernel/wire
            ignore: true
```

Generated code (`*.gen.go`) is excluded from the coverage gate and from lint.
`make generate` regenerates everything and CI fails on drift.

## Rationale

The strict server gives compile-time proof that each flat operation has the required
interface types. An embedded `UnimplementedStrict` can still answer 501, so it does not
prove runtime implementation. The status/principal matrix and native acceptance supply
that evidence; P20 reconciles generated stubs and missing routes with phase ownership
under [0042](0042-capability-scope-and-supersession.md). One spec drives the test clients. Forcing the item-path and
streaming operations through it would mean fighting the generator on every path; writing
them by hand against chi keeps them readable and lets them stream. Keeping them in the
spec preserves one documented contract.

**Alternatives considered:**
- *Everything hand-written*: full control, but no mechanical drift detection for the
  roughly two hundred flat operations.
- *Everything generated with `{path:.*}` patches*: oapi-codegen does not emit catch-all
  patterns and the mode-switch responses would need one operation per mode.
- *huma or another code-first framework*: the Go code would become the contract, and the
  wire shapes are dictated by JFrog anyway.

## Constraints

- The spec is written before the handler. Review checks the spec first.
- oapi-codegen's OpenAPI 3.1 support is partial, so the specs stay on 3.0.3.
- The tagliatelle `overrides` setting requires the version bundled with golangci-lint v2;
  the template pins v2.14.
- The status-code contract is only as good as the research tables; a code that is not in
  the tables is a research task, not a guess.

## Verification

- Server-assembly tests (`internal/app` and `test/server`) start `httptest.NewServer` and
  drive every flat operation through `pkg/client`.
- A test walks `api/*.openapi.yaml` and asserts every `x-handwritten` path is absent from
  the generated router and present in the hand-written one.
- `TestAccHeadAnswersEveryGeneratedGet` calls every generated GET and its HEAD and holds
  the HEAD to the GET's status and header fields, with no body;
  `TestAccHeadAnswersHandwrittenGets` does the same for every route `handwrittenHeads`
  names, as admin, a user without rights and anonymously, and
  `TestUnitHeadDecidedForEveryGet` fails a GET route with no decision about its HEAD.
  `TestAccHeadOfADownloadReadsNothing` holds a folder archive's HEAD to opening no file
  and a skill bundle's to counting no download. `TestAccHeadOnTheWire` does the same over
  HTTP for a JSON list, a repository, the storage API, 404, 401 and 403 refusals and the
  public probes, and keeps 405 for a POST-only operation and a JFrog Router probe.
- A table-driven status-code test over the research inventory.
- Error-response tests per profile.
- Base-URL resolver tests over the four sources for every format that embeds URLs.
- The generation drift check runs in CI.

## References

- [02-artifactory-core-api-inventory](../02-artifactory-core-api-inventory.md)
- [05-platform-identity-and-security-apis](../05-platform-identity-and-security-apis.md)
- [09-jfrog-cli-compatibility](../09-jfrog-cli-compatibility.md)
- `weaveplatform/hostweave@main`, `docs/research/decisions/0012-api-openapi-spec-first.md`
- <https://github.com/oapi-codegen/oapi-codegen>
- <https://github.com/ldez/tagliatelle>
