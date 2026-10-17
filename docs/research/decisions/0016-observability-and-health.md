# 0016: Observability and health

Status: Proposed

Checked against: Artifactory 7.161.32

## Context

Clients and orchestrators probe Artifactory through several health endpoints
(`/api/system/ping`, `/api/v1/system/readiness` and `liveness`,
`/access/api/v1/system/ping`, `/router/api/v1/system/health`). Operators expect metrics
and structured logs. hostweave's decision 0015 defined a telemetry seam with no-op
defaults and test recorders.

## Decision

All documented health endpoints are served with their documented bodies and status codes.
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
