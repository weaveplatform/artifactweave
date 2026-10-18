# API field descriptions

`api/artifactory.openapi.yaml`, `api/access.openapi.yaml` and `api/event.openapi.yaml`
are the API source of truth for the management surface (decision
[0002](../research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md)).
Describe every object property, including inline request/response objects, and every
path/query/header parameter. Generated Go comments come from these files; edit the
source and regenerate rather than editing generated files.

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

`TestOpenAPIDescriptionsDescribeContract` fails any description, summary or `info` text
that names artifactweave, Artifactory or JFrog outside a code span.

## Response-only fields and defaults

Shared resource schemas can contain response-only fields and application defaults
that differ from formal schema requirements. Describe those distinctions explicitly;
changing validation is a separate contract change. Do not imply that a response-only
revision can be submitted to an update operation that does not exist.

OpenAPI 3.0 ignores siblings of a bare `$ref`. Use an `allOf` reference wrapper to
attach a field-specific description without changing the referenced schema.
`TestOpenAPIFieldDescriptions` checks completeness, presence wording and reference
wrappers; accuracy and relationships still require review against implementation.

Run `make generate`, then validate the schema and the generated clients under
`pkg/client/`. Operations marked `x-handwritten: true` stay in the spec for
documentation only; `TestHandwrittenRoutesNotGenerated` asserts the generated router
never claims them.
