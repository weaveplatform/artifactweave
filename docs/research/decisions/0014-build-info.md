# 0014: Build-info

Status: Proposed

## Context

The JFrog CLI's canonical CI loop uploads with `--build-name` and `--build-number`, then
publishes build-info (`jf rt bp`), later promotes a build (`jf rt build-promote`), and
discards old runs (`jf rt build-discard`). Build-info is a JSON document stored both through
`PUT /api/build` and as a file in the `artifactory-build-info` repository (or
`<project>-build-info`). The `builds` AQL domain and several GET searches query it.
[Research 22](../22-aql-searches-build-info.md) §3 holds the requests, responses and
client checks this record relies on.

## Decision

**Endpoints.** `internal/service/build` implements the Builds API in research 22 §3.1, with
the status codes and headers the CLI checks:
- **`PUT /api/build`** accepts the `application/vnd.org.jfrog.artifactory+json` content type,
  answers 200, and sets `X-Checksum-Sha256` to the stored document's SHA-256.
- **Reads:**
  - `GET /api/build` always answers 200, with an empty list when there are no builds;
  - `GET /api/build/{name}` answers 404 for an unknown name;
  - `GET /api/build/{name}/{number}` takes `started`, `project` and `diff`. Without
    `started` it answers the latest run with that number.
- **Changes:**
  - append answers 204;
  - rename answers 200 `text/plain`;
  - promotion takes the `PromotionRequest` content type and answers 200 with `{messages[]}`;
  - retention answers **204** and accepts `count` as a string or a number;
  - both delete forms remove one run per listed occurrence of a number.
- **`GET /api/builds/buildArtifacts/{name}/{number}/{repo}`** answers `[{repo, path, name}]`,
  so the CLI skips its AQL fallback.

**Run identity.** A run is identified by project, name, number and `started`.
- The same name and number may be published more than once, and each publish is a new run.
- The store's unique key is `(project, name, number, started)`.
- `jf rt bp --overwrite` relies on this: it sends a number once per existing run, and each
  occurrence deletes one run.

**The document as an item.**
- **Where it lives.** Each run's document is also written as the item
  `<project>-build-info/<encoded name>/<encoded number>-<started epoch ms>.json`. The project
  segment is `artifactory` when the project is empty or `default`.
- **Encoding.** The name and number use the CLI's encoding (research 22 §3.3), so the CLI's
  `LATEST` and build-append AQL queries find the item.
- **One path for both.** Publishing writes the run record and the item together. Deleting a
  run removes both, so the storage API, AQL and retention see one consistent set. The
  document item is purged rather than trashed, because a restore would bring it back without
  its run.
- **Deleting artifacts.** A delete with `artifacts=1` or `deleteArtifacts` also deletes the
  items that hold an artifact's checksum and carry the run's `build.name` and `build.number`
  properties. These are ordinary item deletes: they go to the trash can, and each needs
  delete permission on the item. A copy without the properties is kept.

**Raw-path routing.** The `{name}` and `{number}` path parameters are matched on the raw,
still-escaped path. A `%2F` in a build name therefore stays inside its segment, as the
CLI's escaped promotion URL requires. No spec extension is needed: chi routes on
`URL.RawPath` when the request has one, and the generated binder path-unescapes each
segment, so the operation receives the name whole.

**Linking artifacts.** The CLI adds `build.name`, `build.number` and `build.timestamp` to
uploaded files as matrix parameters itself, and they are stored as properties like any
other matrix parameter. A build's artifacts and dependencies link to items by checksum,
which is what the AQL build domains join through.

**Permissions** come from the build resource, matched against the build-info repository as
`<name>/**` through the native evaluator
([0006](0006-authorization-native-permission-model.md)). The path matched is the run's
document item path, so a name containing `/` is matched in its encoded form
(`team%2Fapp/**`), and a listing of names shows a name when a permitted path lies below
its folder:

| Operation | Needs |
|---|---|
| Publish | deploy; delete as well to replace an existing document item |
| Read | read |
| Promote | deploy, plus deploy on the target repositories |
| Rename | deploy and delete |
| Retention, delete | delete |

**Promotion** copies or moves the build's artifacts between repositories with the documented
options, then records a promotion on the run:
- `copy` or move;
- `artifacts`, by default true;
- `dependencies`, by default false;
- additive `scopes`;
- `properties`, set with or without a target;
- `failFast` and `dryRun`.

A virtual target resolves to its default deployment repository. Copy and move go through
the same service as the item API, with its conflict and checksum rules.

**No Terraform resources.** Neither JFrog provider manages build-info (research 22 §3.5).

## Rationale

Without build-info the CLI's CI workflow stops at upload, and the `builds` AQL domain and
the build webhook domain have nothing to serve. Storing the document as an item as well as
a record keeps one deletion and retention path, and lets the CLI's AQL lookups work
unchanged.

**Identifying a run by `started`** follows what the CLI does: a build number is not unique,
and treating it as unique would break `jf rt bp --overwrite`.

**Retention answering 204** follows the CLI, which rejects any other status. JFrog documents
200 ([DV-031](../../implementation/deviations.md#documentation-versus-behaviour)).

**Alternatives considered:**
- *Storing build-info only as items:* build queries would need JSON parsing per query.
- *A unique key of project, name and number:* rejects a second run of the same number,
  which Artifactory accepts and the CLI expects.

## Constraints

- Xray-related build fields are accepted and ignored.
- Release bundles created from builds belong to a stubbed product.
- The build-artifacts endpoint is known only from client source; its response shape is
  what the CLI parses.

## Verification

- `jf rt u --build-name --build-number`, `jf rt bp` (including `--overwrite`),
  `jf rt build-promote` and `jf rt build-discard` in the acceptance suite.
- Store tests with several runs of one number, and the encoded item path.
- `TestBuildRoutesKeepEscapedSlash`: an escaped `/` in a build name or number reaches the
  operation intact.
- `builds` domain AQL fixtures.
- Build webhook events observed by the test receiver.

## References

- [22-aql-searches-build-info](../22-aql-searches-build-info.md), §3
- [02-artifactory-core-api-inventory](../02-artifactory-core-api-inventory.md)
- [09-jfrog-cli-compatibility](../09-jfrog-cli-compatibility.md), build rows
- <https://docs.jfrog.com/integrations/reference/jfrog-build-info-api>
