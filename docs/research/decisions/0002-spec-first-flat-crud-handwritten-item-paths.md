# 0002: Spec-first for flat CRUD, hand-written handlers for item paths

Status: Proposed

Checked against: Artifactory 7.161.32

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

**Three specs, strict servers for the flat families only.** `api/artifactory.openapi.yaml`,
`api/access.openapi.yaml` and `api/event.openapi.yaml` (OpenAPI 3.0.3) generate
oapi-codegen v2.8 strict chi servers into `internal/server/gen/{artifactory,access,event}`
and Go clients into `pkg/client/{artifactory,access,event}`. The strict servers cover the
families whose operations have single-segment parameters and one response shape:
repositories, the legacy security shims, Access v2 users, groups and permissions, tokens,
roles, environments, projects, webhooks, cleanup policies and system.

**Hand-written handlers for item paths.** Operations with multi-segment path parameters or
mode-switch responses (artifact deploy and download, storage item info, properties, copy,
move, zap, archive download, trash, replication status, Docker and the other package
protocols, metadata PATCH) are chi handlers in `internal/server/items` and
`internal/format/*`, mounted before the generated mux. They stay in the spec for
documentation with the extension `x-handwritten: true`; a test asserts that the generated
router never claims a path marked that way.

**Content-type normalisation.** Middleware rewrites `application/vnd.org.jfrog.artifactory.*+json`
to `application/json` before the strict decoder, and accepts `application/yaml` on the
configuration PATCH.

**Status-code contract.** The research inventories carry a status-code column per endpoint
taken from the JFrog docs and from the JFrog CLI and Terraform provider sources. Handlers
take their codes from that table, not from convention.

**Error envelopes.** One table in `internal/server/errors`, keyed by surface:

| Surface | Envelope |
|---|---|
| Artifactory REST | `{"errors":[{"status":<int>,"message":"<string>"}]}` |
| Access, Projects | `{"errors":[{"code":"<string>","message":"<string>"}]}` |
| OCI `/v2/` | `{"errors":[{"code","message","detail"}]}` with the OCI spec codes |
| Xray (stub) | `{"error":"<string>"}` |
| npm | `{"error":"<string>"}` |
| Terraform registry | `{"errors":["<string>"]}` |
| Go proxy, PyPI | plain text |
| NuGet | text or XML as the NuGet client expects |

**Base URL resolver.** Embedded absolute URLs (npm `dist.tarball`, Helm `urls`, NuGet
`@id`, Terraform `download_url`, Docker `Location` and `WWW-Authenticate` realm,
file-info `uri` and `downloadUri`, repository `url`) come from one resolver in
`internal/server/baseurl` with Artifactory's precedence (OSS 6.20
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
live only in `pkg/repo` and `internal/wire/{artifactory,access,legacy}` and those packages
are excluded from `tagliatelle`; everything else keeps the template rule.

```yaml
linters:
  settings:
    tagliatelle:
      case:
        rules:
          json: snake
          yaml: snake
        overrides:
          - pkg: pkg/repo
            ignore: true
          - pkg: internal/wire/artifactory
            ignore: true
          - pkg: internal/wire/access
            ignore: true
          - pkg: internal/wire/legacy
            ignore: true
```

Generated code (`*.gen.go`) is excluded from the coverage gate and from lint.
`make generate` regenerates everything and CI fails on drift.

## Rationale

The strict server gives compile-time proof that every flat operation is implemented with
the right types, and one spec drives the clients the tests use. Forcing the item-path and
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

- `internal/server` tests start `httptest.NewServer` and drive every flat operation through
  `pkg/client`.
- A test walks `api/*.openapi.yaml` and asserts every `x-handwritten` path is absent from
  the generated router and present in the hand-written one.
- A table-driven status-code test over the research inventory.
- Error-envelope tests per surface.
- Base-URL resolver tests over the four sources for every format that embeds URLs.
- The generation drift check runs in CI.

## References

- [02-artifactory-core-api-inventory](../02-artifactory-core-api-inventory.md)
- [05-platform-identity-and-security-apis](../05-platform-identity-and-security-apis.md)
- [09-jfrog-cli-compatibility](../09-jfrog-cli-compatibility.md)
- `weaveplatform/hostweave@main`, `docs/research/decisions/0012-api-openapi-spec-first.md`
- <https://github.com/oapi-codegen/oapi-codegen>
- <https://github.com/ldez/tagliatelle>
