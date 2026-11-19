# Virtual folder source-authority failures (#1723)

A virtual HTML folder index must abort when its own current source lookup or final authority check fails. Returning a successful cached/other-member index hides that failure. Ordinary upstream acquisition failures still leave the remote member's live contribution out, preserving the established cached-child fallback.

## Primary evidence and limits

Current [Browsing Artifacts](https://docs.jfrog.com/artifactory/docs/browsing-artifacts) and [Smart Remote Repositories](https://docs.jfrog.com/artifactory/docs/smart-remote-repositories) document optional remote browsing, virtual aggregation and cached browsing content. They do not establish the response to a failed internal source-authority database statement.

The verified [official OSS 7.161.26 source archive](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz), SHA-256 `b867422f3137c542b402c38a2ca2584396ef431b691ca70f038ff5cc3768802c`, provides the phase distinction:

- `backend/core/src/main/java/org/artifactory/repo/RemoteRepoBase.java`, `listRemoteResources`, lines 1608–1647, catches `IOException` around `getChildUrls`. A failed listing contributes no remote children. Database/configuration failures outside that acquisition catch abort composition.
- `web/rest-ui/src/main/java/org/artifactory/ui/rest/service/artifacts/browse/generic/BrowseNativeHtmlService.java`, `execute`, catches exceptions in its HTML chain and sets 404. Therefore this source does not prove a vendor 500 response.

Artifactweave retains its existing sanitized source-authority 500, an observed project contract already visible through direct requests. Equivalence to current closed 7.x wire behavior remains an unresolved compatibility inference [I]. The correction is that failed own authority cannot produce successful partial HTML; the vendor response-code comparison remains unresolved. This research introduces no observed vendor response.

## Independent feedback loops

Baseline `621523d38c1cda8b822fbb1b52a7f4b36e5f7b5d` reproduces the initial defect across Generic and Maven on actual SQLite and PostgreSQL 17.12, twice each. A controlled upstream fills a real cached child and serves a live directory index. Temporarily renaming `remote_sources` inside each isolated test schema makes the next real source lookup fail. Direct browsing returns sanitized 500 and logs the original SQL cause once; virtual browsing returns cached HTML 200. The cache alias still returns its healthy cached child, the request context remains live, and no additional upstream exchange occurs. The table is restored and scratch schemas are independently cleaned up.

A separate deterministic unit loop exercises the final phase through the real `source.Service`: the remote listing succeeds and its body closes, then a later member's child read enables the failure. The member-bound final callback returns a typed store fault, Read refusal or superseded source. The old merger silently dropped that live contribution and returned cached plus healthy-other-member HTML 200. Permanent controls additionally distinguish a live store fault carrying a driver deadline from an ended parent context.

## Bounded implementation policy

- Initial acquisition errors first honor an ended parent context. Trusted own-store faults, typed caller/current-policy refusals and superseded source identity abort composition. A content-open origin marker excludes store promotion; it does not override a typed authority refusal.
- Raw upstream HTTP 401/403/503, connectivity failures, live-parent upstream timeouts, unsupported indexes and ordinary misses retain acquisition fallback. A raw upstream status is not interpreted as a caller Read verdict.
- Every final `FolderListing.Revalidate` error aborts disclosure. These checks own current source/credential/permission authority and perform no upstream acquisition. Cached children and other members cannot mask their failure.
- A private source-phase wrapper preserves causes and uses the existing `SourceError` response mapping. A live own-store fault carries the existing storage marker ahead of timeout mapping. Cached-child and graph-walk errors retain the existing `InternalError` path. No global source classifier, cache publication, freshness or availability policy changes.

### Mixed security-kind limit

`internal/artifacts/adapters/upstream/client.go` classifies TLS certificate verification and destination guards under `remote.ErrSecurity`; `internal/artifacts/core/remote/policy.go` projects it to an HTTP 502 with `FailureKindSecurity`. The kind also covers redirect/option acquisition failures and does not prove current-authority provenance by itself. Initial occurrences retain the existing acquisition fallback because their mixed kind does not establish a trusted authority phase; an identical error from the final authority callback aborts. This does not claim that every producer of that mixed kind corresponds to Java `IOException`. Tests pin both phases and distinguish raw upstream 401/403 from typed `source.ErrDenied`. The fix does not introduce a new producer taxonomy.

## Verification scope

Permanent server controls use isolated real SQLite/PostgreSQL schemas and verify sanitized responses, one original-cause log, no extra origin calls, unchanged source availability state and healthy cache-alias behavior. Unit controls separate initial and final phases, verify member binding and body ownership, and preserve ordinary acquisition fallback. The PR records the final source-matched coverage, race, existing native HTTP controls and quality results; repository-wide 95% coverage remains a separate CI gate.
