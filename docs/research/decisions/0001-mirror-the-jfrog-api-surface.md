# 0001: Mirror the JFrog API surface

Status: Proposed

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on
2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

## Context

artifactweave replaces JFrog Artifactory for teams that want one self-hosted place to
store, proxy and distribute packages and container images. Those teams already have
clients, CI pipelines, the JFrog CLI and Terraform code written against Artifactory's
REST, Access and Event APIs and against the eight package protocols Artifactory serves.
The cheapest migration is one where none of that changes.

JFrog publishes OpenAPI 3.1 definitions for every endpoint, embedded in the documentation
pages and marked proprietary. The surface is large (around 300 endpoints across six JFrog
products) and uneven: some of it is the product, some of it is licence-gated add-ons, and
some of it belongs to products artifactweave does not replace.

## Decision

Wire compatibility with Artifactory 7.x is the product contract:

- The Artifactory REST API under `/artifactory/api`, the Access API under `/access/api`,
  the Event API under `/event/api`, the router health endpoint and the Terraform discovery
  document at the host root are served with JFrog's paths, methods, request and response
  shapes, status codes and error envelopes.
- The eight package protocols (Docker/OCI, npm, Go modules, Maven, PyPI, NuGet, Helm and
  Terraform providers and modules) are served under Artifactory's URL layouts.
- Every JFrog API family is assigned one of four tiers, recorded in
  [10-scope-and-tiers](../10-scope-and-tiers.md):
  - *implement*: full behaviour (repositories, artifacts and storage, searches, security,
    identity, tokens, projects, webhooks, cleanup policies, system, replication, and since
    2026-10-13 Distribution with release bundles v1, cold-storage archive and retention,
    platform federation and topology, federated repositories and Workers, with
    artifactweave-only peers per [0021](0021-peer-trust-and-sync-protocol.md));
  - *shim*: a thin compatible adapter over the implemented model (the legacy
    `/api/security` users, groups, permissions and token endpoints, API keys);
  - *stub*: a well-formed `501` in that product's own error envelope so clients fail
    predictably (Xray, Lifecycle and AppTrust, Evidence);
  - *ignore*: `404` (Pipelines, Mission Control, MyJFrog, the SaaS bridges, JFConnect).
    The one JFConnect exception is the metrics sink
    `POST /jfconnect/api/v1/backoffice/metrics/log`, which answers 200 and discards the
    body (`internal/server/server.go`, DV-011): every `jf` command posts there once the
    server reports 7.102 or later, and accepts 200 or 201
    ([jfrog-cli-core `common/commands/command.go`](https://github.com/jfrog/jfrog-cli-core/blob/25dbeb0335c03f14a284b1a34c654a9ceadf85b5/common/commands/command.go),
    [jfrog-client-go `jfconnect/services/metrics.go`](https://github.com/jfrog/jfrog-client-go/blob/3b4b900d8174ba878d1ce7ef1df8d18fad341472/jfconnect/services/metrics.go);
    [10-scope-and-tiers](../10-scope-and-tiers.md)).
- For every stubbed product the research records the observed client reaction (the JFrog
  CLI and the Terraform providers), cited from client source, so a stub is never assumed
  to be harmless.
- JFrog's OpenAPI definitions are cited by URL and never vendored. artifactweave's own
  `api/*.openapi.yaml` files are authored from the documented behaviour; paths, parameter
  and field names are facts about the wire protocol.
- Artifactory licence tiers do not exist in artifactweave. Features that JFrog gates behind
  Pro, Enterprise or Enterprise+ are implemented when they are in scope; nothing is gated.

## Rationale

Mirroring the surface rather than designing a cleaner API is what makes the replacement
drop-in: the JFrog CLI, the `jfrog/artifactory`, `jfrog/platform` and `jfrog/project`
Terraform providers and every native package client keep working, so there is no client
migration and no need to ship a CLI or provider of our own.

Tiering is explicit because the alternative, implementing whatever a client happens to
hit next, produces an inconsistent surface and silent failures. A `501` in the right
envelope is diagnosable; a missing route that returns HTML is not.

**Alternatives considered:**
- *A native API with a compatibility layer on top*: two surfaces to document and test, and
  the compatibility layer would still have to be complete.
- *Vendoring JFrog's OpenAPI files*: faster to start, but the files are proprietary and
  the embedded definitions are thin and inconsistent across families.

## Constraints

- Compatibility is with the documented behaviour at the research baseline (2026-10-12).
  Behaviour that is undocumented and only observable on a live Artifactory is listed in
  [12-open-questions](../12-open-questions.md) and must be captured before it is fixed in
  code.
- JFrog's wire formats mix camelCase, snake_case and kebab-case field names and
  per-service error envelopes. These are reproduced, not normalised. Booleans follow what
  Artifactory sends rather than JFrog's docs: the repository flags documented as strings
  are written as booleans and accepted in either spelling, because the Terraform provider
  decodes them as booleans (#109,
  [03](../03-repository-configuration-model.md#typing-traps)).
- Where a product synchronises between instances (federation, distribution, Access
  Federation, topology), compatibility covers the management APIs only; the traffic
  between instances is artifactweave's own protocol
  ([0021](0021-peer-trust-and-sync-protocol.md)), so artifactweave does not interoperate
  with Artifactory or Edge peers.
- Features cut from v1 are listed in [12-open-questions](../12-open-questions.md) with the
  reason; a cut feature answers with a clear error, never with a partial implementation.

## Verification

- The acceptance suite drives the real JFrog CLI and the real Terraform providers against
  the binary ([0015](0015-testing-and-acceptance.md)).
- A table-driven test asserts the status code and envelope of every endpoint in the
  inventory, with the research tables as the oracle.
- Stub tier endpoints have a test per product asserting the envelope and the status.
- `TestSpecParity` compares `api/*.openapi.yaml` with a gitignored cache of JFrog's
  published definitions and fails on any gap not listed in `api/parity-baseline.json`;
  the baseline shrinks as endpoints land
  ([research 01, parity oracle](../01-jfrog-api-landscape.md#parity-oracle)).

## References

- [01-jfrog-api-landscape](../01-jfrog-api-landscape.md)
- [02-artifactory-core-api-inventory](../02-artifactory-core-api-inventory.md)
- [05-platform-identity-and-security-apis](../05-platform-identity-and-security-apis.md)
- [10-scope-and-tiers](../10-scope-and-tiers.md)
- [16-enterprise-products](../16-enterprise-products.md)
- <https://docs.jfrog.com/integrations/docs/openapi-specifications>
