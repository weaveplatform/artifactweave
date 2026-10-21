# 0016: Observability and health

Status: Proposed

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on
2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)). **[KB]** marks
a JFrog KB article, as in the [research README](../README.md#target-version-and-sources).

## Context

Clients and orchestrators probe Artifactory through several health endpoints
(`/api/system/ping`, `/api/v1/system/readiness` and `liveness`,
`/access/api/v1/system/ping`, `readiness` and `liveness`, `/router/api/v1/system/health`,
`readiness` and `liveness`). Operators expect metrics
and structured logs. hostweave's decision 0015 defined a telemetry seam with no-op
defaults and test recorders.

## Decision

All documented health endpoints are served with their documented bodies and status codes,
and each probe checks what its service needs
([#403](https://github.com/weaveplatform/artifactweave/issues/403); research 06,
[system endpoints](../06-system-configuration-and-descriptor.md#system-endpoints)):
- `GET /artifactory/api/system/ping` answers `OK` while the database and the binary store
  answer, and 500 with the reason as plain text otherwise
  ([ping](https://docs.jfrog.com/administration/reference/artifactoryping.md): "500":
  "Internal Server Error"). The 6.20 `PingResource` checks storage and Access and answers
  500 with the reason; that 7.x checks the same is **[I]**. `GET /access/api/v1/system/ping`
  checks the database alone.
- `GET /artifactory/api/v1/system/readiness` answers `{"code":"OK"}` while the database and
  the binary store answer, and 500 otherwise, with the reason in the Artifactory error
  envelope ([readiness](https://docs.jfrog.com/administration/reference/getreadinessprobe.md):
  "Returns HTTP error code with reason if not working properly"). The failure body is
  **[I]**: no page shows one.
- The liveness probes answer `{"code":"OK"}` while the process serves requests and check no
  store: an orchestrator restarts a container whose liveness fails, and a restart brings
  back no database or bucket. 7.x documents a 500 for liveness as for readiness
  ([liveness](https://docs.jfrog.com/administration/reference/getlivenessprobe.md)); what it
  checks is **[I]**.
- `GET /router/api/v1/system/health` gives the router and each service a `node_id`, `state`
  and `message`, and names the services by the stored instance id as `jfrt@<uuid>` and
  `jfac@<uuid>`, the id `service_id` returns and the token issuer carries. Access (`jfac`)
  needs the database and Artifactory (`jfrt`) the database and the binary store. A service
  whose probe fails is `UNHEALTHY` with the reason as its message, the router is then
  `UNHEALTHY_PEER`, and the answer is 503. These fields, the 503 and `UNHEALTHY_PEER` come
  from the search-indexed copy of the removed reference page, and the ids from a **[KB]**
  [sample response](https://jfrog.com/help/r/artifactory-how-to-use-artifactory-health-check/sample-response-for-health-check-on-artifactory-ha).
  Which entry carries which unhealthy state, the `OK` message of a healthy entry, and
  `node_id` being the host name, as for a node with no configured id, are **[I]**.
- `/router/api/v1/system/readiness` and `liveness` and `/access/api/v1/system/readiness` and
  `liveness` answer without credentials, because JFrog's Helm charts, Wingman and the secret
  rotator poll them through the public URL
  ([jfrog/charts `stable/artifactory/values.yaml`](https://github.com/jfrog/charts/blob/master/stable/artifactory/values.yaml),
  [jfrog-secret-rotator-lambda `lambda_function.py`](https://github.com/jfrog/jfrog-secret-rotator-lambda/blob/main/secret-rotator/lambda_function.py)).
  They answer as Artifactory's probes do: the router is ready while both of its services
  are, and Access while the database answers. No page documents them, so their bodies are
  **[I]**.

A probe reads the instance identity from the database and asks the binary store whether it
holds one blob, the SHA-256 of empty content, each within 5 seconds. The failure reason
names the part that failed, "The database is not available" or "The binary store is not
available", and nothing more, for every caller: the probes answer without credentials, and
a store's own error can carry a database host and user, a file path or a bucket name. The
cause goes to the access log with the request. Whether 7.x's reason text carries such
detail is **[I]**.

P18 will make `/artifactory/api/v1/metrics` serve Prometheus text with request, storage,
cache, upstream and scheduler metrics; it currently answers `501`. Logs are already
`slog` JSON with request ids, and a request that authenticated names its user in its
access log line, never the credential it sent
([#532](https://github.com/weaveplatform/artifactweave/issues/532)). The full telemetry seam
will follow hostweave 0015: an OpenTelemetry metric and trace API with no-op defaults and
test recorders, so exporters can be configured without code changes. The persistent work
queue already uses an OpenTelemetry meter with no-op defaults and recorder tests
(`internal/service/work`); completing system-wide instrumentation/export remains P18.

P12 owns the configuration logger endpoints. P38 extends that baseline with
application/Distribution metrics, live log and debug controls, lock and background-task
visibility, system information, support bundles and cloud log collection. P35 supplies
the HA state those operators inspect. P41 qualifies
observability across the expanded platform. The
[capability roadmap](../../implementation/capability-roadmap.md) retains these obligations;
the implemented probes, structured logs and usage sinks are not evidence that metric
exporters or operational administration APIs are working.

**Telemetry sinks.** `POST /artifactory/api/system/usage` and
`POST /jfconnect/api/v1/backoffice/metrics/log` receive the usage reports and metrics that
`jf` and the Terraform providers post ([research 06](../06-system-configuration-and-descriptor.md#system-endpoints)).
Both answer 200 and discard a body of up to 1 MiB. A longer body answers 413 in the
Artifactory error envelope: a declared length over the limit is refused before any of the
body is read, and an undeclared one is read only to the limit
([#431](https://github.com/weaveplatform/artifactweave/issues/431)). The reports the clients
send are well under a kilobyte.

Both sinks answer without credentials, and stay that way until the live check on
[#216](https://github.com/weaveplatform/artifactweave/issues/216) settles what 7.x does.
Whether 7.x requires authentication on `/api/system/usage` is **[I]**:
[RTFACT-22629](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-22629), an open request for
an unauthenticated call-home endpoint, lists the current endpoint as "Auth required", but it
has no fix version and no JFrog staff comment, so it does not settle the question. The
clients only log a failed post, so either answer leaves them working.

## Rationale

The endpoints are required for compatibility; Prometheus text is what every operator
already scrapes. Reusing the hostweave seam keeps the platform consistent.

**Alternatives considered:**
- *JFrog's own metrics format only*: it is OpenMetrics text, so the same endpoint serves
  both.

## Constraints

- Metrics never contain repository paths or principals as labels; cardinality is bounded
  to repository keys and package types.

## Verification

- Health endpoint shape tests, and probe tests with a failing database and a failing binary
  store: each probe's status, body and reason, the services router health marks unhealthy,
  and that no answer repeats the cause while the log keeps it.
- Metrics endpoint parse test with the Prometheus text parser.
- Log field tests for request id propagation.
- Telemetry sink tests: a body over 1 MiB answers 413 on each sink and is not read to the
  end; a client's report answers 200.

## References

- [06-system-configuration-and-descriptor](../06-system-configuration-and-descriptor.md), system API
- `weaveplatform/hostweave@main`, `docs/research/decisions/0015-telemetry-seam.md`
