// Package smart is Smart Remote: a remote repository that discovers the
// Artifactory repository behind its URL, copies and refreshes the user
// properties of what it caches, reports its downloads to the peer, records
// when an item is confirmed gone from the origin, and answers transitive
// AQL against the peer. It reads through core remote's shared retrieval and
// cache policy and adds only authenticated, bounded and durable side effects.
//
// # Role
//
// artifacts module, core layer: Smart Remote, beside core remote
// (artifacts/core/remote), which it imports and which never imports it
// (decision 0044). The policy reaches Smart only through the core-owned
// remote.Interceptor and the WithSmart and SmartActive context mark; Smart
// installs itself as Policy.Smart and reaches the upstream through the
// remote.Upstream port and the outbound request hook, never a client's
// fields.
//
// # Inputs
//
//   - Driving: the shared policy, which hands Smart every read whose context
//     carries no WithSmart mark (Read); the router, which wraps every request
//     in SmartTrace; the remote status endpoint (Status); the AQL transitive
//     peer reader, which reads through the source service with a Smart AQL
//     query; and the work queue's workers, which run the queued property
//     refreshes and statistics submissions.
//   - Driven: remote.Policy (its store, item service, credential options,
//     circuit and capacity) and remote.Upstream for the peer requests; the
//     access API's Authenticator and Authorizer, which recheck the principal
//     a queued side effect runs as; the persistent work queue
//     (platform/work) under the work kind the application passes, one for
//     ordinary remotes and one for registry remotes; and the registry-path
//     parser, the docker slice's ParseRegistryPath, which the application
//     passes so that Smart never imports the slice.
//
// # Outputs
//
//   - Smart, the remote.Interceptor of one policy, with Discover, Status and
//     Close; NewSmart, which builds it and starts its workers.
//   - SmartTrace, the router middleware that reads the loop trace, and
//     SmartOriginHeader, the header that carries it.
//   - SmartStatus with SmartRepresentationStatus and SmartWorkStatus, the
//     JSON document of the remote status endpoint, an artifactweave
//     diagnostic rather than an Artifactory wire document.
//
// # Invariants
//
//   - The X-Artifactory-Originated trace fences loops: a request whose trace
//     holds this node, or 8 nodes already, is refused before any peer
//     request.
//   - Smart headers go only to same-origin requests: the outbound hook adds
//     them to the first request and to a redirect that stays on its origin.
//   - Metadata requests are confined to the peer's storage targets, on the
//     first request and on every redirect.
//   - A nil registry-path parser fails closed: Docker, OCI and Helm OCI
//     repositories get no property path and no registry grant.
//   - A statistics claim commits before its submission, so a restart never
//     counts a download twice, and a lost acknowledgement stays uncertain
//     rather than being sent again.
//   - Cache-only, offline and no-network reads make no peer request at all,
//     Smart side effects included (decision 0007).
//   - The work kinds, the header name, the status document's fields and the
//     error texts stay byte-identical.
//
// # References
//
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (Smart Remote synchronisation).
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md
//     (work kinds are code constants).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Names the migration introduces" (Interceptor, WithSmart).
//   - docs/implementation/smart-remote.md (evidence and wire choices) and
//     docs/operations/smart-remote.md (the operations guide);
//     https://github.com/weaveplatform/artifactweave/issues/634 and
//     https://github.com/weaveplatform/artifactweave/issues/636 (transitive
//     AQL).
//   - JFrog Smart Remote documentation:
//     https://docs.jfrog.com/artifactory/docs/smart-remote-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 220 and lines 644 to 645 (core/remote/smart; smart_peer.go stays
//     in core remote, a recorded deviation) and line 1012 (the mapping row).
//   - Plan: docs/implementation/plan.md#p9-remote-repositories and
//     #p9d-modular-monolith-migration, epic RMT
//     (https://github.com/weaveplatform/artifactweave/issues/942).
//   - https://github.com/weaveplatform/artifactweave/issues/1025
//
// # Status
//
// Implemented P9. Staged in P9d.
//
// # Testing
//
//   - smart_test.go: property refresh and ownership, statistics claims,
//     origin absence, the flags, authority rechecks and the status document.
//   - smart_durable_test.go: retries, capacity, lost acknowledgements,
//     observation order, discovery throttling, no-store and credential
//     revisions across the durable queue.
//   - smart_fault_test.go: discovery protocol failures, shared discovery,
//     property document admission, the metadata target and redirect fence,
//     and the loop bound.
//   - smart_aql_test.go: the transient authenticated AQL POST and its
//     admission, failure and boundary cases.
//   - smart_listing_test.go: Smart listings through real discovery and the
//     cache fences.
//   - smart_registry_test.go (package smart_test): the registry grant and
//     property path with the docker slice's parser, and a nil parser; it
//     reads Smart's helpers through export_test.go.
//   - smart_fixture_test.go holds the policy fixture these tests share,
//     copied from core remote's external tests.
package smart
