// Package hex validates Hex tarballs and extracts metadata needed by the
// signed registry v2 protocol. It never executes metadata.config as Erlang:
// a bounded parser reads only the term forms the Hex package metadata
// specification prescribes. It also encodes and verifies the registry's
// signed resources (names, versions and packages/NAME), so a repository can
// serve its own signed view and trust an upstream registry's only once its
// signature verifies.
//
// # Role
//
// artifacts module, ecosystem layer: the Hex tarball validation, metadata
// and registry index rules of the Hex slice. Its Handler plugs into the
// item service through the format hooks (decision 0032); the slice's http
// package (artifacts/ecosystem/hex/http) serves the signed registry from
// the tarballs it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for hex repositories to Handler, and the work
//     queue, which runs every format's reindex through it; and the
//     protocol, which lists a repository's files (Files), reads stored and
//     upstream tarballs (InspectTarball) and builds or verifies the signed
//     resources.
//   - Driven: the format hooks' environment (format.Env), whose blob store
//     Handler reads every tarball from and through whose store it keeps
//     the package index (artifacts/core/pkgindex); and the generic deploy
//     policy (artifacts/ecosystem/generic), which Handler embeds and
//     applies first.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that admit only a valid tarball at the path its
//     own name and version give (Inspect, Admit), refuse a deploy of the
//     generated registry resources, index each stored tarball (Deployed,
//     Reindex) and class each path (Classify).
//   - The package index: a package per Hex package name, with a version
//     per stored release.
//   - The rules the protocol shares: Package, Requirement, InspectTarball,
//     TarballPath, IsTarballPath, DerivedPath, ValidName, ValidVersion and
//     Files; and the registry codec: RegistryPackage, EncodePackage,
//     EncodeNames, EncodeVersions, DecodePackage, DecodeNames,
//     DecodeVersions, ParsePublicKey and PublicKeyPEM.
//
// # Invariants
//
//   - A tarball is admitted only when both tar layers, its inner checksum,
//     its contents' paths and links and its metadata verify, and its name
//     and version give exactly the path it is written to.
//   - Every signed resource is a gzip-compressed Signed message of at most
//     16 MiB whose signature is RSA PKCS #1 v1.5 over the payload's
//     SHA-512, made or checked with a key of at least 2048 bits; a decoder
//     reads no field of the payload before its signature verifies.
//   - Every size, count and nesting depth is bounded before it is used.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks; the protocol and storage decision); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/implementation/hex-client.md (connecting Mix, the signing keys,
//     and the remote's checks of upstream signatures and checksums).
//   - JFrog Hex repositories:
//     https://docs.jfrog.com/artifactory/docs/hex-repositories, and the Hex
//     registry schema the codec follows:
//     https://github.com/hexpm/specifications/blob/main/registry/package.proto.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/hex), line 379 (the slice row) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/767 and
//     https://github.com/weaveplatform/artifactweave/issues/1037
//
// # Status
//
// Implemented P9c, which delivered the plan's P30 group (issues #762 to
// #768). Staged in P9d.
//
// # Testing
//
//   - package_test.go and terms_test.go: the public decimal and ecto
//     tarballs, traversal, duplicate and link refusals in the nested
//     archive, and the metadata.config parser's accepted forms and bounds.
//     testdata/README.md records where every fixture came from; the
//     acceptance steps also serve its telemetry tarball.
//   - registry_test.go: hex.pm's signed packages/decimal resource and
//     public key, round trips of every signed resource, and malformed
//     fields.
//   - coverage_test.go and security_coverage_test.go: admission and
//     reindex, hostile and truncated wire fields, forged signature
//     envelopes and crafted outer archives.
package hex
