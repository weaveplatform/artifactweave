// Package cdn signs download URLs on a CDN in front of an object store
// (decision 0029). A blob store that has a CDN takes one of these signers in
// its Options and answers blob.ViaCDN with the signer's URL for the blob's
// object key, so the store is never wrapped and keeps its other interfaces.
//
// Every signer issues URLs on time windows: a URL minted in a window expires
// when the window ends plus the TTL, so a URL lives at least the TTL and at
// most twice it, and every client in one window is handed the same URL for
// a blob when the signature is deterministic.
//
// # Role
//
// platform layer: CDN URL signing for the blob stores. No module owns a CDN,
// so the signers sit in the platform beside platform/blob, whose ViaCDN
// route they serve. The s3 store takes a CloudFront and the gcs store a
// CloudCDN as its Signer; the azure store takes a FrontDoor and mints the
// SAS itself, with the start and expiry that Window gives. The composition
// root builds them from the blob.<backend>.cdn configuration, which
// ParseDomain also validates. It imports platform/blob, the standard library
// and the AWS SDK's CloudFront signer.
//
// # Inputs
//
//   - Provider configuration, read at start: CloudFrontConfig (the
//     distribution host, the ID of a public key in one of its trusted key
//     groups, the private key and the hash), CloudCDNConfig (the load
//     balancer host, the key name on the backend bucket and the key) and
//     FrontDoorConfig (the endpoint host). Each takes PathPrefix, the CDN
//     origin's path in storage, which its URLs leave out, and the two
//     signers take a clock.
//   - Keys: ParsePrivateKey reads a CloudFront key in PEM, RSA as PKCS#1 or
//     PKCS#8 or ECDSA as SEC 1 or PKCS#8, and ParseCloudCDNKey a Cloud CDN
//     key file, 16 bytes in base64url.
//   - When a download redirects, the blob's object key under the bucket
//     root, which the stores export as ObjectKey, and a Request: the TTL,
//     and the file name and media type that travel as response overrides. A
//     FrontDoor takes the storage path "<container>/<blob name>" and the SAS
//     query the azure store minted.
//
// # Outputs
//
//   - Signer (Provider, Covers, Sign) and its two implementations,
//     CloudFront (NewCloudFront) and CloudCDN (NewCloudCDN).
//   - FrontDoor (NewFrontDoor; Provider, Covers, URL), which is not a
//     Signer: Front Door checks no signature, so it only moves the SAS that
//     the azure store mints onto the endpoint.
//   - Expiry and Window: when a URL issued now expires, and the start of its
//     window, which an Azure SAS takes as its start time.
//   - ParseDomain, a CDN host as a base URL, and SignCloudCDN, Google's
//     signing algorithm on its own, with which tests sign URLs for an edge.
//   - The constants HashSHA1, HashSHA256, CloudCDNKeySize and
//     CloudFrontMaxSize, the largest file a redirect sends to CloudFront.
//   - ErrConfig, which every configuration error wraps.
//
// # Invariants
//
//   - A store with a CDN answers blob.ViaCDN with the signer's URL and is
//     never wrapped in a CDN decorator: a wrapper hides the store's other
//     interfaces, blob.PartSizer among them, which silently changes how the
//     mirror sizes its chunks (issue 39).
//   - URLs are issued on time windows. Windows are TTL long and start on a
//     multiple of the TTL since the Unix epoch, and a URL issued in one
//     expires at the window's end plus the TTL, in whole seconds, because
//     every CDN states expiry in epoch seconds. So a URL lives between one
//     and two TTLs, and where the signature is deterministic (RSA, HMAC-SHA1
//     and the window-aligned SAS a FrontDoor carries) every client in a
//     window gets the same URL for a blob, which the edge can cache. An
//     ECDSA signature differs each time. A TTL under a second is refused.
//   - CloudFront and Cloud CDN sign the response overrides with the rest of
//     the URL, so an edge refuses a URL whose file name was edited. A space
//     in an override is sent as %20, never "+", which object stores read
//     literally.
//   - Covers refuses a store prefix that the CDN origin's path does not
//     contain, and the stores call it when they are built, so a CDN that
//     cannot reach a store's blobs fails at start. Sign and URL refuse a key
//     outside that path. Both refusals wrap ErrConfig.
//   - Every error text starts "cdn:" and stays byte-identical.
//
// # References
//
//   - Decision 0029: docs/research/decisions/0029-cdn-delivery-and-download-redirect.md
//     ("Signing contract", the CDN configuration and the time windows).
//   - Research 19: docs/research/19-cdn-and-download-redirect.md (the three
//     providers' signing, and CloudFront's 50 GB cacheable size).
//   - https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/private-content-signed-urls.html,
//     https://docs.cloud.google.com/cdn/docs/using-signed-urls and
//     https://learn.microsoft.com/en-us/azure/frontdoor/scenario-storage-blobs.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules": the platform imports no module.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 152 (platform/cdn) and line 1050 (the mapping row).
//   - Plan: docs/implementation/plan.md#p4b-reference-deployments-and-cdn-delivery
//     and #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/981
//
// # Status
//
// Implemented P4b: the CloudFront signer, the time windows and ParseDomain
// (issue 120), the Cloud CDN signer (issue 121), FrontDoor and Window (issue
// 122), and CloudFrontMaxSize, which keeps a file over CloudFront's
// cacheable size off the edge (issue 402). Staged in P9d.
//
// # Testing
//
//   - TestUnitExpiryIsTheWindowEndPlusTheTTL and TestUnitWindow: the window
//     arithmetic, and a TTL under a second refused.
//   - TestUnitParseDomain: hosts and test edge URLs accepted; a path, query,
//     fragment, user or other scheme refused.
//   - TestUnitCloudFrontURLsRepeatWithinAWindow and
//     TestUnitCloudCDNURLsRepeatWithinAWindow: one URL per blob in a window,
//     and another in the next.
//   - TestUnitCloudFrontRefusals and TestUnitCloudCDNRefusals: unusable
//     configuration and keys, a key outside the path prefix and a zero TTL,
//     each with ErrConfig.
//   - TestUnitSignCloudCDNMatchesGooglesSample and
//     TestUnitCloudCDNSignsTheOverridesAndStripsThePathPrefix: URLs checked
//     against vectors computed with Google's Python sample.
//   - TestUnitFrontDoorURL and TestUnitFrontDoorOriginPath: the SAS carried
//     unchanged and the origin path stripped.
//   - TestAccCloudFrontURLsPassTheEdge, for every key form and hash, and
//     TestAccCloudCDNURLsPassTheEdge: signed ranged GETs through a cdntest
//     edge, with the overrides; the Cloud CDN edge refuses unsigned, edited,
//     unknown-key and expired URLs.
//
// The s3, gcs and azure stores sign through these types in their own tests.
// Through cdntest edges, the acceptance feature cdn.feature runs jf rt dl
// and crane pull, and cdn_front_door.feature runs jf rt dl.
package cdn
