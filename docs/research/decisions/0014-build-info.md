# 0014: Build-info

Status: Proposed

## Context

The JFrog CLI's canonical CI loop uploads with `--build-name` and `--build-number`, then
publishes build-info (`jf rt bp`) and later promotes a build. Build-info is a JSON
document stored both through `PUT /api/build` and as a file in the
`artifactory-build-info` repository (or `<project>-build-info`), and the `builds` AQL
domain and several GET searches query it.

## Decision

`internal/service/build` implements `PUT /api/build` (accepting the
`application/vnd.org.jfrog.artifactory+json` content type and returning the codes the CLI
accepts), the build runs and build detail reads, build deletion, retention and rename,
build promotion with the `PromotionRequest` content type, and the
`buildArtifacts` searches. Uploads carrying `build.name` and `build.number` properties are
linked to the build. Build-info documents are also written to
`artifactory-build-info/<name>/<number>-<timestamp>.json` so the storage API, AQL and
retention see them as items.

Build promotion copies or moves the build's artifacts between repositories with the
documented options (`copy`, `dependencies`, `failFast`, `dryRun`, `properties`) and sets
the promotion status on the build.

## Rationale

Without build-info the CLI's CI workflow stops at upload, and the `builds` AQL domain and
the build webhook domain have nothing to serve. Storing the document as an item as well
as a record keeps one deletion and retention path.

**Alternatives considered:**
- *Storing build-info only as items*: build queries would need JSON parsing per query.

## Constraints

- Xray-related build fields are accepted and ignored.
- Release bundles created from builds belong to a stubbed product.

## Verification

- `jf rt u --build-name --build-number`, `jf rt bp`, `jf rt bpr` in the acceptance suite.
- `builds` domain AQL fixtures.
- Build webhook events observed by the test receiver.

## References

- [02-artifactory-core-api-inventory](../02-artifactory-core-api-inventory.md)
- [09-jfrog-cli-compatibility](../09-jfrog-cli-compatibility.md), build rows
- <https://docs.jfrog.com/integrations/reference/jfrog-build-info-api>
