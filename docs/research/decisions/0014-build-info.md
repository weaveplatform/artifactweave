# 0014: Build-info

Status: Proposed

Checked against: **Artifactory 7.161.32** on 2026-10-17 ([research 22 section 6](../22-aql-searches-build-info.md#6-artifactory-7x-check), [#341](https://github.com/weaveplatform/artifactweave/issues/341)); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

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
  - promotion takes the `PromotionRequest` content type and answers 200 with `{messages[]}`,
    or 400 with them when a fail-fast promotion met an error or a warning;
  - retention answers **204**, accepts `count` as a string or a number, and takes `project`
    and `async`;
  - both delete forms remove one run per listed occurrence of a number.
- **`GET /api/builds/buildArtifacts/{name}/{number}/{repo}`** answers `[{repo, path, name}]`,
  so the CLI skips its AQL fallback.

**Run identity.** A run is identified by project, name, number and `started`.
- The same name and number may be published more than once, and each publish is a new run.
- The store's unique key is `(project, name, number, started)`.
- `jf rt bp --overwrite` relies on this: it sends a number once per existing run, and each
  occurrence deletes one run.

**Duplicate entries.** A publish whose module lists one file twice is refused with 400, as
Artifactory 7.x refuses it by default from 7.90.15 (RTDEV-47671).
- **What counts.** Two artifacts of one module with the same name, path and checksums, or
  two dependencies of one module with the same id and checksums. Checksums compare without
  regard to case.
- **What does not.** The same file in two modules, an artifact the same module also lists as
  a dependency, and files that share a checksum under another name, path or id. An entry
  without checksums names no file (a folder, say), so it is not checked.
- **The answer.** 400 with `the build-info module '<id>' lists the <artifact|dependency>
  '<name>' more than once`, before anything is stored.
- **Append** checks the modules it adds, not those the run already has.
- **The switch.** `build.block_duplicate_entries` (default `true`) is Artifactory's
  `artifactory.build.block.duplicate.entries`; `false` accepts such a publish.

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
| Promote | deploy; each file also needs what its copy or move needs, and annotate for properties |
| Rename | deploy and delete |
| Retention, delete | delete, on the build and on each run removed |

**Promotion** acts on the latest run of the number, following Artifactory's source
(research 22 §3.6):
- **What moves.** The artifacts are the items holding an artifact's checksum that carry the
  run's `build.name` and `build.number` properties, in `sourceRepo` when one is named; the
  item named like the artifact is preferred. Dependencies are found by checksum in any
  repository, limited by `scopes`. `artifacts` defaults to true, `dependencies` to false.
  An artifact or dependency that is nowhere is an error line.
- **Files sharing a checksum.** Each artifact or dependency takes an item no other entry of
  the run has taken while one is left, so several dependencies with one SHA move several
  files (7.125.4), as do a run's artifacts held at different paths with one checksum. An
  entry for the same file (an artifact's name and path, a dependency's id) takes the same
  item, and artifacts and dependencies are matched apart, so a dependency on another
  module's artifact is that artifact's file. The build artifacts search finds files the same
  way.
- **Folder entries.** An entry without checksums, such as one naming a folder, is found
  nowhere and reported nowhere (7.161.15).
- **Where it goes.** Each file is copied or moved to `targetRepo` at the path it had,
  through the item API's copy and move, with its conflict and overwrite rules. A virtual
  target resolves to its default deployment repository; a missing target is 404 and one that
  takes no deploys is 400. A file already in the target stays, with an info line. Without a
  target nothing moves.
- **Per-file permissions.** A file the caller may not read, delete for a move, or deploy at
  the target, a move out of a remote cache, and an overwrite without delete are error lines,
  not refusals, as Artifactory reports them.
- **Properties** replace their keys' values on the files where they landed, or where they
  are without a target. A file the caller may not annotate is a warning line.
- **The record.** The promotion is recorded only with a `status`, and not in a dry run. It
  keeps `timestamp` (or now), the caller, `ciUser` (or the caller), the comment, the source
  repository and the resolved target, where the `build.promotions` AQL domain finds it.
- **Fail-fast**, the default and what the JFrog CLI sends, stops at the first error or
  warning and answers 400. The promotion runs in one transaction, so a fail-fast promotion
  that stops is rolled back whole and its report keeps only the error and warning lines
  ([DV-032](../../implementation/deviations.md#api-behaviour)). Without fail-fast, what can
  be done is done and the status is recorded; the answer is 200.
- **A dry run** plans each file and checks each permission, and changes nothing.

**Retention** discards the runs of a build that its settings do not keep, each as a delete
of that run would: its document goes, and its artifacts too with `deleteBuildArtifacts`.
- **Which runs.** Newest first by `started`, the runs past the newest `count` and those
  started before `minimumBuildDate`, less every run of a number in
  `buildNumbersNotToBeDiscarded`. Those protected runs still count towards `count`, as
  Jenkins's log rotation counts builds kept forever (research 22 §3.6). A retention request
  for a build with no runs is 404.
- **Values.** `count` of 0 is 400, as in Artifactory; absent or negative keeps any number.
  `minimumBuildDate` is a timestamp or epoch milliseconds.
- **Folder entries.** Artifacts are found by checksum, so an entry naming a folder deletes
  nothing (7.161.15).
- **`async`** absent or false discards before answering. True checks the request and the
  caller's delete permission, answers 204, and discards in the background, planning again
  then; a failure there is logged.
- **A published `buildRetention`** is stored as the build name's retention and applied once,
  after the run is stored, as the publisher. A publisher without delete keeps its publish and
  discards nothing, with a warning in the log. Later publishes without settings discard
  nothing, and a retention request records no lasting policy.

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

**Promotion and retention follow Artifactory's source over JFrog's pages** where they
differ: fail-fast's 400, upper-case message levels, the refused `count` of 0 and `async`
defaulting to false
([DV-033](../../implementation/deviations.md#documentation-versus-behaviour)).

**Rolling a fail-fast promotion back** rather than leaving what it had done
([DV-032](../../implementation/deviations.md#api-behaviour)): Artifactory's partial
promotion leaves a build's files split between two repositories, which the JFrog CLI's
own help warns of. One transaction makes fail-fast mean "all or nothing", which is what a
pipeline that stops on the error assumes.

**Duplicate entries are counted per module and list,** by what names one file: build-info
gives an artifact a name and a path, and a dependency an id. Artifactory documents only
"the same artifact" used more than once. Counting across modules would refuse every
multi-module build, which lists a shared dependency in each module, and counting by
checksum alone would refuse a build holding one file at two paths, which 7.161.15 says one
build may (RTDEV-71186). The JFrog CLI merges its own records the same way, artifacts by
name and SHA-1 and dependencies by id and SHA-1 (`build-info-go`, `mergeArtifacts`), so a
CLI publish never meets the refusal.

**Alternatives considered:**
- *Storing build-info only as items:* build queries would need JSON parsing per query.
- *A unique key of project, name and number:* rejects a second run of the same number,
  which Artifactory accepts and the CLI expects.

## Artifactory 7.x check

7.x changed four points ([research 22 section 6](../22-aql-searches-build-info.md#6-artifactory-7x-check)). [#353](https://github.com/weaveplatform/artifactweave/issues/353)
brought this record up to date:
- A publish with duplicate entries fails by default, unless
  `artifactory.build.block.duplicate.entries` is false (7.90.15): **Duplicate entries**
  above.
- Promotion promotes every dependency, even those that share a checksum (7.125.4): **Files
  sharing a checksum** above.
- Entries that name a folder are ignored (7.161.15): **Folder entries** above.
- A failed build-info search by artifact answers 400 (7.161.15). The build artifacts search
  already answers 400 to every request it cannot run (no build name, no number or status,
  both, a mapping that is not a regular expression) and 404 when it finds nothing; only a
  store failure is 500.

What 7.x documents of the duplicate rule is "the same artifact used in the build more than
once", with no status or message, so the rule above, its 400 and its message are inferred,
as are the claim rule for files sharing a checksum and the check on append
([#216](https://github.com/weaveplatform/artifactweave/issues/216)).

The audit against KB, Jira and client sources on 2026-10-18
([#378](https://github.com/weaveplatform/artifactweave/issues/378)) found where the Builds
API differs from 7.x. [#412](https://github.com/weaveplatform/artifactweave/issues/412)
fixes each, and corrects this record's text when it lands:
- 7.x differs: `PUT /api/build` answers 204 with `X-Checksum-Sha256`
  ([uploadbuild-1](https://docs.jfrog.com/integrations/reference/uploadbuild-1.md); its twin
  [uploadbuild](https://docs.jfrog.com/integrations/reference/uploadbuild.md) still says
  200), as Artifactory 6.20's `BuildResource.addBuild` does, where artifactweave answers
  200; the JFrog CLI accepts 200, 201 or 204; fixed by #412.
- 7.x differs: `PUT /api/build` takes an optional `buildRepo` query parameter, "the
  build-info repository key that stores the build", where artifactweave ignores it and
  stores the run in the project's build-info repository
  ([uploadbuild-1](https://docs.jfrog.com/integrations/reference/uploadbuild-1.md)); fixed
  by #412.
- 7.x differs: `GET /api/build/{name}/{number}?diff=<older number>` answers the Builds Diff
  of the two runs, where artifactweave ignores `diff` and answers the run
  ([getbuildinfo](https://docs.jfrog.com/integrations/reference/getbuildinfo.md)); fixed by
  #412.
- 7.x differs: rename answers `Build renaming of '<from>' to '<to>' was successfully
  started.` ([renamebuild](https://docs.jfrog.com/integrations/reference/renamebuild.md));
  fixed by #412.
- 7.x differs: both delete forms answer `The following builds has been deleted
  successfully: '<name>#<number>', ...`
  ([deletebuilds](https://docs.jfrog.com/integrations/reference/deletebuilds.md)); fixed by
  #412.

## Constraints

- Xray-related build fields are accepted and ignored.
- Release bundles created from builds belong to a stubbed product.
- The build-artifacts endpoint is known only from client source; its response shape is
  what the CLI parses.

## Verification

- `jf rt u --build-name --build-number`, `jf rt bp` (including `--overwrite`),
  `jf rt build-promote` (dry run, copy with properties, move from one source repository)
  and `jf rt build-discard` (with a protected number) in the acceptance suite.
- Service tests for every promotion line, the fail-fast rollback, the retention arithmetic
  and a publish's `buildRetention`; handler tests for the media types, the 400 report and
  the background discard.
- `TestDuplicateEntries` and `TestAppendDuplicateEntries` for what counts as a duplicate
  and the switch, `TestBuildDuplicateEntries` for the 400;
  `TestPromoteFilesSharingAChecksum` and `TestFolderEntries` for promotion, the build
  artifacts search and retention.
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
