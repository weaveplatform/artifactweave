// Package hexhttp serves the signed Hex registry v2 protocol under
// /artifactory/api/hex/{repoKey}: the repository's public key, the signed
// names, versions and packages/NAME resources, and package tarballs, each
// for GET and HEAD. Every registry resource is generated from the releases
// the caller may read and signed with the repository's own RSA key, so Mix
// trusts the repository rather than any registry behind it. Tarball
// validation and the registry codec are the format package's
// (artifacts/ecosystem/hex); this package answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native signed Hex registry v2
// protocol of the Hex slice. It is named hexhttp so that it does not shadow
// net/http. Outside tests, only its own http subtree, the module's http
// adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type hex, local, remote or
//     virtual. A tarball is published through the generic item routes, not
//     through this protocol; the format's Handler admits it there.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, reads items and blobs
//     and writes errors; the key pairs contract in Options (access/api),
//     which reads the RSA key pair a repository signs with (its
//     primaryKeyPairRef, or a virtual repository's keyPair); the remote
//     source service (Options.RemoteSources), which fetches upstream
//     registry resources and tarballs into the remote cache; and the format
//     package.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class and the Artifactory error profile.
//   - Answers: the public key as PEM; names, versions and packages/NAME,
//     signed; and tarballs: a local repository's stored item, a remote's
//     upstream tarball once it matches its signed release, or the first
//     member of a virtual repository, in order, that holds it.
//
// # Invariants
//
//   - A registry resource lists only releases the caller may read, and
//     keeps only the advisories those releases reference, so a denied or
//     shadowed version discloses nothing.
//   - A signing key that is not configured, cannot be read, is not RSA or
//     is shorter than 2048 bits fails closed with 503.
//   - A remote reads upstream registry resources only once their signature
//     verifies against its hexPublicKey, and delivers a tarball only when
//     its name, version and inner checksum match the signed release, and
//     its outer checksum too where the release carries one. A release
//     without one gets the checksum of its verified tarball, since current
//     Mix clients refuse a release without it; a packages/NAME request
//     completes at most 64 such releases and fails beyond that.
//   - A dependency on the registry a release came from is rewritten to
//     name the serving repository, so transitive resolution stays behind
//     its permissions; a dependency on another registry keeps its
//     destination.
//   - A virtual repository's members are distinct Hex local or remote
//     repositories, and the first member that holds a version supplies it.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/implementation/hex-client.md (connecting Mix, the signing keys,
//     and the remote's checks of upstream signatures and checksums).
//   - JFrog Hex repositories:
//     https://docs.jfrog.com/artifactory/docs/hex-repositories, and the Hex
//     self-hosting guide: https://hex.pm/docs/self-hosting.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/hex) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/767,
//     https://github.com/weaveplatform/artifactweave/issues/1037 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P9c, which delivered the plan's P30 group (issues #762 to
// #768). Staged in P9d.
//
// # Testing
//
//   - hex_test.go: the tarball path grammar, the rewriting of same-registry
//     dependencies, advisory compaction, and the remote tarball checks at
//     the protocol boundary and in the source validator.
package hexhttp
