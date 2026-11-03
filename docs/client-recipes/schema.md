# Recipe source contract

Each file in [src/](src/) contains one JSON object with `schema: 1`. The
[`clientrecipes` tool](../../tools/clientrecipes/main.go) is the validating
decoder. Unknown fields, missing implemented formats and operation classes
outside the registered inventory fail the check. The filename must match the
repository package type, for example `npm.json`.

| Field | Meaning |
|---|---|
| `type`, `name` | Registered package type and human-readable client/format name |
| `client` | Generated client pin label and runtime/compatibility qualifications; edit its template in `tools/depsync/catalog.json` |
| `version_sources` | Repository-relative paths containing the exact checked client pins |
| `endpoint` | Native endpoint or discovery URL, with `${AW_URL}` and, where applicable, `${AW_REPO}` placeholders |
| `auth` | Mechanism, subject/scope/permission requirements and any explicit anonymous-read prerequisite |
| `fresh_home` | How to isolate client configuration, caches and credentials for a cold connection check |
| `prerequisites` | Package/project inputs, runtime dependencies and administrator configuration required before using the commands |
| `configure` | Ordered code snippets for the selected client |
| `publish`, `install` | Operation objects with `classes`, `note` and ordered `steps` |
| `limitations` | Format-specific proof boundaries and unsupported operations |
| `evidence` | Executable source references: `path` plus exact `contains` anchors to the relevant setup, native operation or denial assertion |

A snippet has `language`, `code` and an optional explanatory `note`. A note
identifies the filename for configuration-file snippets. Shell snippets may
refer to environment variables; other languages retain their own expansion
rules. Templates are documentation data. Do not evaluate them on a server or
interpolate untrusted values into shell source.

Operation `classes` uses the inventory letters: `L` local, `R` remote, `V`
virtual and `F` federated. An unavailable operation has `classes: ""`,
`steps: []` and a note explaining why no command is offered. An available
operation must have at least one step. Classes can be narrower than the
registered inventory when native-client proof is missing; the limitations
explain that difference. The consumer must not infer support by copying an
operation's commands to an unlisted class.

`evidence-lock.json` maps all referenced client/evidence paths to SHA-256
digests. This makes changes to a pinned client, test or fixture visible even
when the short source anchors still exist. Regenerate only after reviewing
the affected instructions. Neither anchors nor digests establish that a
command has executed successfully: retain the actual test run and commit
alongside the [verification record](README.md#verification).

The generated Markdown is a human-readable view of these objects. P39 should
consume the JSON and use the [browser handoff](README.md#p39-set-me-up-handoff)
for repository selection, tokens, safe substitution and copy behavior. Schema
changes must update the decoder, renderer, sources and this document together.
