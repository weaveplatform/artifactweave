// Package fakeupstream provides deterministic, loopback-only HTTP fixtures
// for remote repository tests: chi fakes of upstream registries with fault
// modes, request recording, a clock, barriers, TLS and a restricted forward
// proxy. It models upstream mechanics, not observed Artifactory behavior.
// Keep ecosystem documents in the adapter tests that understand their
// schemas. README.md, beside this file, is the user guide, with a worked
// example.
//
// # Role
//
// artifacts module, test support at the adapter layer: controlled upstream
// registries for remote tests. Test files in any module may import it
// (dependency rule 1); production code never does. It is a package of its
// own beneath the upstream transport (artifacts/adapters/upstream), whose
// tests, like core remote's and the slices', send to its servers; it
// imports only chi and the standard library, never the transport, so that
// any test may use it.
//
// # Inputs
//
//   - The resources a test sets with Server.Set and removes with
//     Server.Delete: a body and headers, a modification time and ETag, an
//     explicit status, method or redirect, and a Fault.
//   - The faults a resource carries: a Barrier before the headers or before
//     the body, a dropped connection, a body cut off after a number of
//     bytes, and a falsified Content-Length.
//   - The Options fixed at New: a TLS configuration, which turns on HTTPS
//     and can require a client certificate; the clock, often Clock.Now; a
//     Basic or Bearer Auth policy; and the fixture secrets to redact.
//   - For NewProxy, the proxy's Auth policy and the running fixtures it may
//     reach.
//
// # Outputs
//
//   - An HTTPS or HTTP server on a loopback listener: Server.URL, a Client
//     that trusts the server's certificate, and that Certificate for the
//     transport under test.
//   - Recorded requests: Requests returns redacted snapshots in arrival
//     order, Count counts them by method and path, and Request.HeaderMatches
//     checks a header value the test knows without the record holding it.
//   - NewProxy: a forward proxy that forwards plain HTTP and tunnels CONNECT
//     to the listed fixtures only. ClientIdentity: a test CA and client
//     certificate for mutual TLS.
//
// # Invariants
//
//   - Nothing here connects beyond the fixtures a test started: the proxy
//     refuses any other destination, so a fault test cannot turn into a
//     public registry fetch.
//   - No network timing depends on sleeps: a Barrier holds a request until
//     the test releases it, and Clock controls timestamps, not timers.
//   - A request serves the resource snapshot it read when it arrived; a Set
//     or Delete that runs while it is in flight changes later requests only.
//   - Recorded requests never hold a body, URL userinfo, query values, or
//     credential and cookie header values, so they are safe to print in a
//     failed assertion.
//   - New and NewProxy register Close with the test's cleanup, and Close
//     cancels the requests in flight, so a handler held at a barrier that
//     was never released cannot hang the end of the test.
//
// # References
//
//   - README.md in this directory: the user guide.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md,
//     "Fault injection" (lines 70 to 75): chi fakes of each upstream
//     registry with the same fault modes as the handler fakes.
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md,
//     "Verification": remote behaviour is proven against controlled
//     upstreams, which do not establish parity with a live Artifactory.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules" (rule 1: test files may import any test-support
//     package across modules).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 256 (adapters/upstream/fakeupstream, the controlled upstream for
//     tests), line 652 (the remote section) and line 995 (the mapping row).
//   - Plan: docs/implementation/plan.md#p9-remote-repositories, task 4, and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - docs/testing/ci.md, "Controlled upstream tests".
//   - https://github.com/weaveplatform/artifactweave/issues/615,
//     https://github.com/weaveplatform/artifactweave/issues/618 and
//     https://github.com/weaveplatform/artifactweave/issues/974
//
// # Status
//
// Implemented P9 and P9c. P9 built the fixtures, their fault modes, the
// recorder, the clock, barriers, TLS and mutual TLS, and the forward proxy
// (https://github.com/weaveplatform/artifactweave/issues/615); its upstream
// isolation work let CONNECT tunnel to plain HTTP fixtures too
// (https://github.com/weaveplatform/artifactweave/issues/618). P9c added
// Resource.Method, for the POST of a Git fetch
// (https://github.com/weaveplatform/artifactweave/issues/771). Moved here in
// P9d, beneath the transport that
// https://github.com/weaveplatform/artifactweave/issues/1026 moved into the
// parent directory.
//
// # Testing
//
//   - The self-checks are TestAcc tests, since each starts loopback servers
//     and checks HTTP behaviour (docs/testing/ci.md, "Go test kinds"); they
//     need no Docker. They cover resources, validators and ranges, every
//     fault, authentication, recording and cookies, redirects, TLS and
//     mutual TLS, barriers under concurrent mutation, and the proxy with its
//     live tunnels.
//   - TestAccApplicationEndpointOnly checks the wiring the README asks of
//     native-client tests, and TestAccMutableArtifactAndMetadata checks
//     expiry, mutation, interruption and retry for both fixture kinds.
//   - The package is in the normal coverage gate, with no exclusion.
package fakeupstream
