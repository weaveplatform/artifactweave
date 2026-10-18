# 0016: Observability and health

Status: Proposed

Checked against: Artifactory 7.161.32; audited against KB, Jira and client sources on
2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)). **[KB]** marks
a JFrog KB article, as in the [research README](../README.md#target-version-and-sources).

## Context

Clients and orchestrators probe Artifactory through several health endpoints
(`/api/system/ping`, `/api/v1/system/readiness` and `liveness`,
`/access/api/v1/system/ping`, `/router/api/v1/system/health`). Operators expect metrics
and structured logs. hostweave's decision 0015 defined a telemetry seam with no-op
defaults and test recorders.

## Decision

All documented health endpoints are served with their documented bodies and status codes,
except in three ways that remain until
[#403](https://github.com/weaveplatform/artifactweave/issues/403) lands. Today every probe
answers 200 without checking the stores, and the router health document is a constant:
- 7.x differs: ping answers 500 when Artifactory is not working, and readiness and liveness
  answer an error status with the reason
  ([ping](https://docs.jfrog.com/administration/reference/artifactoryping.md),
  [readiness](https://docs.jfrog.com/administration/reference/getreadinessprobe.md),
  [liveness](https://docs.jfrog.com/administration/reference/getlivenessprobe.md)); fixed by
  [#403](https://github.com/weaveplatform/artifactweave/issues/403).
- 7.x differs: router health gives the router and each service a `node_id`, `state` and
  `message`, names each service by its real `jfrt@<id>` or `jfac@<id>` id, and answers 503
  when a service is unhealthy (the search-indexed copy of the removed reference page, and a
  **[KB]** [sample response](https://jfrog.com/help/r/artifactory-how-to-use-artifactory-health-check/sample-response-for-health-check-on-artifactory-ha)
  for the ids; see [research 06](../06-system-configuration-and-descriptor.md#system-endpoints)); fixed by
  [#403](https://github.com/weaveplatform/artifactweave/issues/403).
- 7.x differs: 7.x also serves readiness and liveness under `/router/api/v1/system/` and
  `/access/api/v1/system/`, which JFrog's Helm charts and secret rotator poll
  ([jfrog/charts `stable/artifactory/values.yaml`](https://github.com/jfrog/charts/blob/master/stable/artifactory/values.yaml));
  fixed by [#403](https://github.com/weaveplatform/artifactweave/issues/403).

`/artifactory/api/v1/metrics` serves Prometheus text with request, storage, cache,
upstream and scheduler metrics. Logs are `slog` JSON with request ids. The telemetry seam
follows hostweave 0015: an OpenTelemetry metric and trace API with no-op defaults and test
recorders, so exporters can be configured without code changes.

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

- Health endpoint shape tests.
- Metrics endpoint parse test with the Prometheus text parser.
- Log field tests for request id propagation.

## References

- [06-system-configuration-and-descriptor](../06-system-configuration-and-descriptor.md), system API
- `weaveplatform/hostweave@main`, `docs/research/decisions/0015-telemetry-seam.md`
