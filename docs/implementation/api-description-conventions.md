# API field descriptions

The editable fragments in `api/src/<service>/*.yaml` are the API source of truth for
the management surface (decision
[0002](../research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md)).
The service families are `artifactory`, `access`, `event`, `distribution`, `grid`,
`pfed` and `worker`; `make generate` first bundles each into `api/<service>.openapi.yaml`.
Describe every object property, including inline request/response objects, and every
path/query/header parameter. Generated Go comments come from these descriptions; edit
the fragments and regenerate rather than editing bundles or generated Go files.

Each field description starts with `Required within this object.` or
`Optional within this object.`, matching the containing schema's `required` list.
A required child is required only when its optional parent is supplied. Parameters
start with their presence and location, for example `Optional query parameter.`

Follow presence with a concrete explanation of purpose. Include applicable:

- Units, valid formats, defaults, and the meaning of omission versus zero or empty.
- Create/update conditions beyond the schema's presence rules.
- Referenced resource and identifier type, including scope or location constraints.
- Dependencies, mutually exclusive fields, and effects of selecting a mode or type.
- Server ownership, secret handling, or read/write direction.

For example, a remote repository's `password` is optional in the schema but required
when `username` is set; a virtual repository's `defaultDeploymentRepo` must name one
of its own `repositories`; a repository created with `projectKey` must also carry
`environments`. Explain these relationships at the affected fields rather than
leaving the reader to infer them. Where a boolean is also accepted as a string (see
[research 03](../research/03-repository-configuration-model.md)), say so at the field
and state what the server echoes.

## Describe the contract, not the comparison

A description states what this API accepts and returns, in the present tense. It does not
name artifactweave, Artifactory or JFrog as the subject of a behaviour, compare the two,
or carry history, release versions, the reason for a choice or what a client happens to do
with a field. Those belong in the [research](../research/README.md), the decision records
and the [deviations register](deviations.md), which cite their evidence.

- Write "Returns one entry for this instance", not "Artifactory lists one per node;
  artifactweave is a single process and returns one".
- Where a client's needs shape the contract, state the contract: "One licence key object,
  or an array of them", not the story of which client sends which.
- A response code the server never sends keeps its entry, because the parity oracle
  compares response codes; describe it as "Not returned." or by the condition it would
  answer.
- An unimplemented operation says "Not implemented; answers 501."
- Literal identifiers and values stay, in code spans: `X-JFrog-Download-Redirect-To`,
  `/artifactory/api/storage`, `jfrt@`, a licence type of `artifactweave`. So do the `x-oracle-source` and `x-unverified`
  extensions, which are metadata rather than descriptions.

`TestAccOpenAPIDescriptionsDescribeContract` fails any description, summary or `info` text
that names artifactweave, Artifactory or JFrog outside a code span.

## Response-only fields and defaults

Shared resource schemas can contain response-only fields and application defaults
that differ from formal schema requirements. Describe those distinctions explicitly;
changing validation is a separate contract change. Do not imply that a response-only
revision can be submitted to an update operation that does not exist.

OpenAPI 3.0 ignores siblings of a bare `$ref`. Use an `allOf` reference wrapper to
attach a field-specific description without changing the referenced schema.
`TestAccOpenAPIFieldDescriptions` checks completeness, presence wording and reference
wrappers; accuracy and relationships still require review against implementation.

Run `make generate`, then validate the schema and the generated clients under
`pkg/client/`. Operations marked `x-handwritten: true` stay in the spec for
documentation only; `TestAccHandwrittenRoutesNotGenerated` asserts the generated router
never claims them.

## Retry, cost and paging classes

Every operation declares three classes, as decision 0043's
[Rules for every operation](../research/decisions/0043-api-reliability.md#rules-for-every-operation)
set out. This section is the author's checklist; the decision holds the reasons.

- **`x-idempotency`**: `safe`, `natural`, `key` or `none`, the first that holds in
  that order.
  - A `natural` operation lists the request elements that name its target in
    `x-idempotency-natural`: `path:<name>`, `query:<name>` or `header:<name>` for a
    parameter it declares (`Accept`, `Content-Type` and `Authorization`, which
    OpenAPI forbids declaring, may be named all the same); `body:<JSON Pointer>`,
    empty for the whole body, or `body:archive`, on an operation with a request
    body; or `route` as the only entry.
  - A `none` operation gives `x-idempotency-reason`: `secret-response`,
    `protocol-reconciliation`, `external-effect` or `read-or-write`.
  - Only a `key` operation the server honors a key for references
    `#/components/parameters/IdempotencyKey`. No operation declares
    `Idempotency-Key` itself.
- **`x-cost`**: `ordinary`, `expensive`, `exempt` or `recovery`, the class the
  boundary admits the operation's method and path under today
  (`expensiveRequest`, `HealthRequest` and `APIRecoveryRequest` in `internal/app`
  and the modules' HTTP adapters), stubs included. An `ordinary` operation lists in
  `x-cost-expensive-query` the query parameters whose presence makes it expensive
  (`list` on the storage paths).
- **`x-pagination`** on every operation that answers a collection: `cursor`,
  `offset`, `page`, `native` or `complete`. `x-pagination-parameters` maps
  `position` and `size` to the operation's own parameter names, with a `position`
  on `cursor`, `offset` and `page` and nothing on `complete`. `x-pagination-next`
  is `link` or `body:<JSON Pointer>` on `cursor`, `offset` and `page`, `none` on
  `complete`, and any of the three on `native`. An operation that answers no
  collection has no `x-pagination`.

`make scaffold` writes the three classes as `TODO(author)`; a placeholder may stay
only while the operation carries `x-draft`.

### The 429 and 503

`make bundle` adds the shared `TooManyRequests` as the 429 and `ServiceUnavailable`
as the 503 of every operation that is not `exempt` (see
[`api/src/README.md`](../../api/src/README.md)), so do not declare them by hand.
An operation whose path admission answers in another envelope than its service's
`ErrorResponse` declares its own 429 and 503 in that envelope, each referencing the
shared `RetryAfter`, `RateLimit` and `RateLimitPolicy` headers. Today these are the
retention, import and export, and Docker list operations, whose responses are in
`api/src/artifactory/components-common.yaml`. An operation's own 503 for a
condition of its own, such as a size bound, stays as it is, without a required
`Retry-After` where the condition is not one to retry. No response other than a 429
or 503 declares `RateLimit` or `RateLimit-Policy`.

### The guard

`TestAccAPIPrinciples` (`internal/app/api_principles_test.go`) holds every
operation of the seven bundles to these rules, and a failure prints one summary
grouped by rule and spec. `api_principles_fixtures_test.go` has a fixture that
fails each rule.

| Rule | Fails when |
| --- | --- |
| `class-missing` | `x-idempotency` or `x-cost` is missing. |
| `class-todo` | A class is still `TODO(author)` on an operation without `x-draft`. |
| `class-value` | A class holds a value outside its list. |
| `natural-identifier` | A `natural` operation lists no identifier or one it does not have, or another class lists one. |
| `none-reason` | A `none` operation gives no listed reason, or another class gives one. |
| `key-parameter` | An operation declares `Idempotency-Key` itself, or references the shared parameter without being `key`. |
| `cost-admission` | `x-cost` or `x-cost-expensive-query` disagrees with the admission of the operation's method and an example path. |
| `admission-responses` | An admitted operation lacks its 429 or 503, or an `exempt` one references the shared ones. |
| `admission-headers` | An operation's own 429, or own 503 in another envelope, lacks a shared header reference, a rate header is declared inline, or another response declares `RateLimit` or `RateLimit-Policy`. |
| `admission-envelope` | An operation whose path admission answers in another envelope references the shared 429 or 503. |
| `collection-pagination` | A read whose success body is a top-level array, or that takes a paging parameter and answers an array, has no `x-pagination`. |
| `cursor-next` | A `cursor` operation names no `link` or `body:` next pointer. |
| `pagination-declaration` | `x-pagination-parameters` or `x-pagination-next` does not fit the class, or appears without `x-pagination`. |

A schema cannot tell an envelope's item array from an array inside one resource, so
beyond the two cases `collection-pagination` checks, whether an operation answers a
collection is the author's call under the decision's definition.

`make api-ledger` derives the ledger's `retry`, `admission` and
`pagination_execution` from the classes (see the
[API reliability review](../research/api-reliability/README.md#operation-ledger)).
