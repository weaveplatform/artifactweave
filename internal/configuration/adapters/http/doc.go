// Package configurationhttp is the configuration module's HTTP adapter: the
// artifactory spec's system operations (version, license, service id,
// readiness, liveness and the reverse proxy configuration), the probes and
// telemetry sinks the router mounts beside them, and the YAML descriptor
// PATCH.
//
// # Role
//
// configuration module, HTTP adapter layer. The composition root mounts
// what this package exports; nothing else in the module serves HTTP. It
// applies the descriptor only through configuration/api, imports no server
// or router package and no artifacts or access package, and takes the
// error profile of each path from the request, where the router puts it.
//
// # Inputs
//
//   - Driving: the router, which embeds SystemAPI in the artifactory spec's
//     strict server beside the generated stubs, mounts the probe, sink,
//     router health and descriptor handlers, and installs the error profile
//     rules the handlers write their errors with (httpkit.ErrorProfile).
//   - Deps, from the composition root: the instance store, the binary
//     store, the compatibility version and revision, the base URL,
//     configapi.Settings, the configuration API and the logger.
//
// # Outputs
//
//   - SystemAPI, built by New over Deps: the system operations; the probe
//     handlers ArtifactoryPing, AccessPing, AccessReadiness, RouterReadiness
//     and RouterHealth; and PatchConfiguration.
//   - Liveness, SinkHandler, and SinkBodyLimit, the bound of the sinks'
//     bodies.
//   - The router's contributions: PublicRoutes, the system half of the
//     routes that answer without credentials, and HealthRequest, the probes
//     that the request limits and the API restriction check let pass.
//
// # Invariants
//
//   - The probe paths answer without credentials (PublicRoutes) and
//     outside the request limits and API restrictions (HealthRequest), so
//     that orchestrators and clients reach them before they authenticate
//     and while the server is busy.
//   - A failed probe names the part that failed and never the store's own
//     error, which can carry a host, a user, a path or a bucket; the cause
//     goes to the access log. The liveness probes probe no store.
//   - The descriptor PATCH keeps its strict parse and answers 400 for an
//     invalid descriptor (decision 0009).
//   - Statuses, envelopes and error and log text stay byte-identical,
//     because JFrog's clients and the operators' logs read them.
//
// # References
//
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md
//     (the descriptor PATCH).
//   - Decision 0016: docs/research/decisions/0016-observability-and-health.md,
//     lines 24 to 37 (the probes and their answers).
//   - Decision 0005: docs/research/decisions/0005-authentication-and-tokens.md
//     (the compatibility version the version operation reports).
//   - Research 10: docs/research/10-scope-and-tiers.md (what the server
//     serves, with this package's system.go as evidence).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules": a module reaches another only through its api).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 302 to 314 (configuration/adapters/http).
//   - https://github.com/weaveplatform/artifactweave/issues/1081
//
// # Status
//
// Implemented P0 to P2 as the server root's system handlers (version,
// license, service id, readiness and liveness), with the descriptor PATCH
// (P3), the reverse proxy configuration of the Docker access methods (pull
// request 241) and the probes and router health (issue 403) added later.
// P9d split them into the system partial (WEB-07) and moved them here
// (WEB-11).
//
// # Testing
//
// The unit tests here call the handlers directly over in-memory stores,
// without the router. The route suite test/server/configuration drives the
// probes, the service id and the descriptor PATCH through the router, and the
// router's guards in app (its status, principal and ledger tables) reach the
// system operations there too.
package configurationhttp
