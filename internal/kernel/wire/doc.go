// Package wire holds the JFrog JSON forms that artifactweave exchanges field
// for field: the legacy v1 and v2 permission targets, the Access v2
// permission, the legacy and Access user and group documents, the proxy
// descriptor, the Smart Remote statistics submission, the JFrog CLI's
// multipart upload status and the blob mirror additions to the storage
// summary.
//
// # Role
//
// kernel layer: the JFrog wire forms (camelCase, snake_case and kebab-case
// mixed as JFrog sends them), which converters and handlers map to and from
// the domain model. It declares types only and imports nothing, so the
// generated artifactory server and client, the converters and the handlers
// can all name these types without depending on the domain model.
//
// # Inputs
//
// None at run time: the package declares types only.
//
// # Outputs
//
//   - BlobMirror and the types it holds: the generated artifactory server
//     and client type the storage summary's blobMirrors field with it,
//     named through x-go-type-import in
//     api/src/artifactory/paths-storage.yaml.
//   - The permission target, permission, user and group forms, which the
//     domain model's converters map to and from one internal shape per
//     entity (decision 0020), and which the security handlers decode and
//     encode.
//   - StorageSummaryMirrors for the storage summary, MultipartUploadStatus
//     for the JFrog CLI's multipart upload status poll, ProxyConfiguration
//     for the stored proxy descriptor that the remote resolver reads, and
//     SmartStatistics for Smart Remote synchronization.
//
// # Invariants
//
//   - JSON tags are the wire contract that JFrog's clients read (the JFrog
//     CLI, jfrog-client-go and the JFrog Terraform providers), so a tag
//     never changes to satisfy a linter. Tags use JFrog's names, and
//     artifactweave's own additions to a JFrog document follow that
//     document's case. .golangci.yml exempts this package from
//     tagliatelle's snake_case rule, and decision 0002 limits the exception
//     to the wire models.
//   - Booleans whose default is not false are pointers, so a converter can
//     tell an absent field from false when a client omits it.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md,
//     "Wire models and the linter".
//   - Decision 0020: docs/research/decisions/0020-domain-model.md, "One
//     internal representation per entity" (wire forms live only in the wire
//     packages, and converters produce and consume them).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Public types and published api surface" and "Dependency rules" (rule
//     2: pkg/client may import this package and artifacts/api/repo, and
//     platform/gen/** may import the same two as a named exception).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 118 to 125 (kernel/wire, the JFrog JSON structs) and line 1059
//     (the mapping row).
//   - Plan: docs/implementation/plan.md#p2-stores (task 0),
//     #p4-identity-and-authorization and #p9d-modular-monolith-migration,
//     epic PLT (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/976
//
// # Status
//
// Implemented P2 to P9: the permission target, permission, user and group
// forms with the domain model in P2 (#3), which the security APIs use from
// P4; the blob mirror summary in P2b (#66, #87); the multipart upload
// status in P3 (#91); the proxy descriptor (#645) and the Smart Remote
// statistics (#675) in P9. Moved here in P9d.
//
// # Testing
//
// The package has no tests of its own, since it holds no logic. The tests
// of the code that encodes each form pin its JSON:
//
//   - The permission, user and group forms: the domain model's round trips
//     (TestUnitPermissionV1RoundTrip, TestUnitPermissionV2RoundTrip,
//     TestUnitPermissionAccessRoundTrip, TestUnitUserLegacyRoundTrip,
//     TestUnitUserAccessRoundTrip, TestUnitGroupRoundTrips) and
//     TestUnitWireFormsEvaluateAlike.
//   - StorageSummaryMirrors: TestAccStorageInfoReportsMirrors.
//   - MultipartUploadStatus: TestAccMultipartUploadProtocol.
//   - ProxyConfiguration: TestAccResolverProxyConfiguration.
//   - SmartStatistics: TestAccSmartStatisticsClaimedOnceAndUncertainty.
package wire
