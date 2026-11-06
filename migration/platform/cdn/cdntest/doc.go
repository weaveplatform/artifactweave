// Package cdntest is a fake CDN edge for tests (decision 0029). It checks a
// provider's URL signature the way the real edge does, then serves the blob
// the URL names from a blob store, honouring Range and the response
// overrides, so a download redirect is tested end to end without a cloud.
//
// The edge listens on "localhost" by default. A registry under test listens
// on 127.0.0.1, so Go clients, the JFrog CLI among them, treat the edge as
// another host and drop their Authorization header on the redirect, as they
// do for a real CDN; the edge records whether one arrived.
//
// # Role
//
// platform layer, test support for platform/cdn: a fake edge that stands in
// for CloudFront, Cloud CDN and Azure Front Door. Only tests import it, and
// the test files of any module may (decision 0044, dependency rule 1). It
// imports platform/blob, chi, the AWS SDK's CloudFront policy and the
// standard library.
//
// # Inputs
//
//   - Options, for Start: a Validator, either CloudFront (the trusted public
//     keys by key pair ID) or CloudCDN (the raw keys by key name), and the
//     origin, a blob.Store that serves each request's blob by the SHA-256
//     that ends its path; optionally the host to listen on and the clock
//     that signatures are checked against.
//   - FrontDoorOptions, for StartFrontDoor: the storage endpoint the edge
//     forwards to, such as Azurite's account URL, and the route's cache cap;
//     optionally the host, the cache's clock and the client that forwards.
//
// # Outputs
//
//   - Start and StartFrontDoor, which return a running Edge. Its URL is the
//     base URL to configure as the CDN domain; Requests returns each request
//     it answered as a Request (method, path, Range, status, whether an
//     Authorization header arrived and whether a Front Door edge answered
//     from its cache); Close stops it.
//   - The Validator interface, which CloudFront and CloudCDN implement.
//   - ErrSignature, which every refused signature wraps.
//
// # Invariants
//
//   - An edge from Start checks each request's signature at its clock
//     before it answers, and refuses a missing, bad, unknown-key or expired
//     one with 403, as the real edge does. It models CloudFront's canned
//     policies and Cloud CDN's whole-URL signatures, and refuses a custom
//     policy or a URLPrefix signature. Cloud CDN itself passes an unsigned
//     request to the private origin, which refuses it; the fake refuses it
//     at the edge with the same status.
//   - It serves the blob with Range, HEAD and the response overrides
//     (response-content-disposition and response-content-type), and answers
//     404 for a path that names no stored blob and 502 when the origin
//     fails. Only GET and HEAD are routed.
//   - An edge from StartFrontDoor checks nothing, as Front Door does: it
//     forwards the path and the query, which carries the SAS, to its
//     origin, which checks it, and never forwards Authorization. It caches a
//     whole-object GET that the origin answers 200 under the full request
//     URI, SAS included, for the route's cap or the response's max-age,
//     whichever is shorter, and keeps serving it after the SAS has expired:
//     the exposure decision 0029 bounds. Ranged and conditional requests
//     always reach the origin, a no-store or private response is not
//     cached, and the real edge's 8 MiB chunks are not modelled.
//   - Every edge listens on "localhost" unless given a host, so a client
//     that reached the registry at 127.0.0.1 drops its credentials on the
//     redirect, and the edge records whether an Authorization header
//     arrived.
//
// # References
//
//   - Decision 0029: docs/research/decisions/0029-cdn-delivery-and-download-redirect.md
//     ("Verification": the fake edge; the Front Door cache cap).
//   - Research 19: docs/research/19-cdn-and-download-redirect.md (what each
//     edge checks, and what the Front Door fake does not model).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules" (rule 1).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 153 (platform/cdn/cdntest) and line 1050 (the mapping row).
//   - Plan: docs/implementation/plan.md#p4b-reference-deployments-and-cdn-delivery
//     and #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/981
//
// # Status
//
// Implemented P4b: the edge with the CloudFront validator (issue 120), the
// CloudCDN validator (issue 121) and the Front Door edge with its cache
// (issue 122). Staged in P9d.
//
// # Testing
//
//   - TestAccEdgeServesSignedRequestsAndRecordsThem: a signed GET and HEAD
//     are served and recorded, with whether Authorization arrived, and a
//     POST is refused.
//   - TestAccEdgeRefusesBadSignatures and
//     TestAccCloudCDNEdgeRefusesBadSignatures: an unsigned, tampered,
//     unknown-key, misplaced, unmodelled or expired signature, or one for
//     another path, gets 403; Cloud CDN signatures pass with or without
//     padding.
//   - TestAccEdgeOriginErrors: 404 for a path that names no blob and for a
//     blob the origin lacks, and 502 for a failing origin.
//   - TestAccStartNeedsAValidatorAnOriginAndAHost: Start refuses missing
//     options and an address it cannot listen on, and listens on localhost
//     by default.
//   - TestAccFrontDoorPassesTheSASThrough: the origin accepts or refuses
//     the SAS, and Authorization never reaches it.
//   - TestAccFrontDoorCachesOnTheFullURL and
//     TestAccFrontDoorHonoursCacheControl: the cache key, the cap, max-age,
//     no-store and private, and a cached copy that outlives its SAS.
//   - TestAccFrontDoorOriginErrors: no origin, an address it cannot listen
//     on, and a dead or unusable origin, which answers 502.
//
// The cdn package's TestAccCloudFrontURLsPassTheEdge and
// TestAccCloudCDNURLsPassTheEdge, the azure store's TestAccContractOnAzurite
// (a Front Door edge in front of Azurite), and the acceptance features
// cdn.feature and cdn_front_door.feature run real signers through these
// edges.
package cdntest
